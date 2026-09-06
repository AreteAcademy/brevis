package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// The stored format, and it is a contract: changing it later demands a
// migration, and a credential migration is the one nobody wants to do in a
// hurry.
//
//	brevis-cred/1\n<nonce 12 bytes><ciphertext+tag>   AES-256-GCM
//	brevis-cred/1p\n<value>                           in the clear
//
// Both carry a version on the first line for the same reason: a reader that
// does not recognize the version treats it as absent and falls back to the
// seed, rather than failing -- during a rollout the same store holds both.
const (
	formatEncrypted = "brevis-cred/1"
	formatPlaintext = "brevis-cred/1p"
)

// CredentialBox wraps and unwraps the credential.
//
// The zero value writes in the clear. With a key, AES-256-GCM.
//
// The cipher is OPTIONAL because the real control is the storage's: a dedicated
// bucket, with IAM for a single service account and public access blocked,
// already settles who reads. An application key would protect against somebody
// who has read access and not the key -- but the key lives in the same secret
// the tasks do, so whoever reads the bucket has it too. Calling that security
// would be theatre, and theatre is worse than absence because it ends the
// conversation.
//
// In a directory the recommendation flips: a directory is easier to end up
// shared than a bucket with IAM.
type CredentialBox struct {
	gcm cipher.AEAD
}

// NewCredentialBox settles the key. Empty returns a box that writes in the
// clear.
func NewCredentialBox(key string) (CredentialBox, error) {
	if strings.TrimSpace(key) == "" {
		return CredentialBox{}, nil
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return CredentialBox{}, fmt.Errorf("credential store: the key is not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return CredentialBox{}, fmt.Errorf("credential store: the key decodes to %d bytes, and "+
			"AES-256 needs 32. Generate one with: head -c 32 /dev/urandom | base64", len(raw))
	}

	block, err := aes.NewCipher(raw)
	if err != nil {
		return CredentialBox{}, fmt.Errorf("credential store: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return CredentialBox{}, fmt.Errorf("credential store: %w", err)
	}
	return CredentialBox{gcm: gcm}, nil
}

// Encrypted says whether there is a key.
func (e CredentialBox) Encrypted() bool { return e.gcm != nil }

// Seal produces the bytes that go to the store.
func (e CredentialBox) Seal(value string) ([]byte, error) {
	if !e.Encrypted() {
		return append([]byte(formatPlaintext+"\n"), value...), nil
	}

	nonce := make([]byte, e.gcm.NonceSize())
	// Drawn on every write. Reusing a nonce with the same key in GCM breaks the
	// cipher, and it is the most common mistake of somebody doing this for the
	// first time.
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("credential store: nonce: %w", err)
	}

	out := append([]byte(formatEncrypted+"\n"), nonce...)
	return e.gcm.Seal(out, nonce, []byte(value), []byte(formatEncrypted)), nil
}

// Open returns the stored value, or "" when there is no usable one.
//
// An unknown version, a truncated payload, or one that does not decrypt are all
// "there is no value": the caller falls back to the seed and the run goes on.
// Failing here would trade a possibly stale credential for none at all.
func (e CredentialBox) Open(raw []byte, where string) string {
	header, body, ok := firstLine(raw)
	if !ok {
		slog.Warn("credential store: ignoring a stored value with no version line",
			"store", where, "falling_back_to", "Credential.Value")
		return ""
	}

	switch header {
	case formatPlaintext:
		return string(body)

	case formatEncrypted:
		if !e.Encrypted() {
			slog.Warn("credential store: the stored value is encrypted and no key is set",
				"store", where, "falling_back_to", "Credential.Value")
			return ""
		}
		n := e.gcm.NonceSize()
		if len(body) < n+e.gcm.Overhead() {
			slog.Warn("credential store: the stored value is truncated",
				"store", where, "falling_back_to", "Credential.Value")
			return ""
		}
		clear, err := e.gcm.Open(nil, body[:n], body[n:], []byte(formatEncrypted))
		if err != nil {
			// A swapped key, corruption or tampering. Which one is not said:
			// telling them apart would hand whoever tampers an oracle.
			slog.Warn("credential store: the stored value does not decrypt",
				"store", where, "falling_back_to", "Credential.Value")
			return ""
		}
		return string(clear)

	default:
		slog.Warn("credential store: ignoring a version this build does not read",
			"store", where, "version", header, "falling_back_to", "Credential.Value")
		return ""
	}
}

// warned makes sure the "it is in the clear" line comes out ONCE per store, and
// not on every pipeline run in the same process -- a repeated warning becomes
// noise, and noise is how a warning stops being read.
var warned sync.Map

// WarnIfPlaintext logs once that this store writes without a cipher.
func WarnIfPlaintext(e CredentialBox, where string) {
	if e.Encrypted() {
		return
	}
	if _, already := warned.LoadOrStore(where, true); already {
		return
	}
	slog.Info("credential store: writing in the clear",
		"store", where,
		"why", "no key is set",
		"protection", "whatever guards the store itself -- bucket IAM, directory permissions",
		"to_encrypt", "set the store's Key, or "+EnvCredentialKey)
}

// firstLine splits the header off from the rest.
func firstLine(b []byte) (string, []byte, bool) {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}
