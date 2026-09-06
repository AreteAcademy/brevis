package core

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CredentialStore keeps the rotated value between runs.
//
// Two methods, and that is on purpose: an abstraction with a single
// implementation is a guess about the second one. When a networked store
// exists, its shape will teach things that today would be invented -- what can
// be done now is to not stand in the way, by keeping the read and the write
// behind these two.
type CredentialStore interface {
	// Load returns the stored value. Absent returns ("", nil): there is no
	// value is not an error, and the caller falls back to the seed.
	Load() (string, error)

	// Save writes the rotated value.
	Save(value string) error

	// Describe names the store for the log, revealing nothing of the value.
	Describe() string
}

// CredentialStoreChecker is implemented by a store that can refuse invalid
// configuration before the run starts.
//
// Optional, so that a third-party store need not implement it, and used by
// Credential.Check: finding out that the credential would not have been stored
// AFTER the whole load is too late to act on.
type CredentialStoreChecker interface {
	CheckStore() error
}

// Names of the variables the platform injects.
const (
	EnvCredentialDir = "BREVIS_CREDENTIAL_DIR"
	EnvCredentialKey = "BREVIS_CREDENTIAL_KEY"
)

// FileStore keeps the credential in an encrypted file inside a directory
// somebody supplied.
//
// The SDK learns neither Kubernetes, nor GCS, nor a database: it opens a file.
// Who mounts the volume is the platform's problem, and that is what lets the
// same feature run in ./.brevis on somebody's laptop.
type FileStore struct {
	// Name is the file name, without an extension. Required.
	//
	// It comes from the caller and never from the URL: a URL carries secrets
	// in its query string, and a file name leaks into logs, listings and
	// backups.
	Name string

	// Dir is the directory. Empty falls back to BREVIS_CREDENTIAL_DIR; empty
	// in both turns the store off, saying in the log that it did.
	Dir string

	// Key is the 32-byte key, in base64. Empty falls back to
	// BREVIS_CREDENTIAL_KEY; empty in both writes in the clear, saying once in
	// the log that it is in the clear.
	//
	// For a directory the recommendation is to USE one: a directory is easier
	// to end up shared than a bucket with IAM. What protects the value when
	// there is no key is mode 0700, and nothing else.
	Key string
}

// CheckStore satisfies CredentialStoreChecker: it refuses at assembly time,
// and logs once when the store ends up off.
func (f FileStore) CheckStore() error {
	file, err := f.resolve()
	if err != nil {
		return err
	}
	if file == nil {
		slog.Info("credential store is off",
			"reason", "neither FileStore.Dir nor "+EnvCredentialDir+" is set",
			"effect", "the rotated credential lives for this run only")
		return nil
	}
	WarnIfPlaintext(file.env, file.path)
	slog.Debug("credential store is on", "file", file.path)
	return nil
}

// Describe names the file, never its contents.
func (f FileStore) Describe() string {
	dir := f.Dir
	if dir == "" {
		dir = os.Getenv(EnvCredentialDir)
	}
	if dir == "" {
		return "file store (off)"
	}
	return filepath.Join(dir, f.Name+".cred")
}

// Load returns the stored value, or "" when the store is off.
func (f FileStore) Load() (string, error) {
	file, err := f.resolve()
	if err != nil || file == nil {
		return "", err
	}
	return file.Load()
}

// Save writes the value. With the store off it does nothing and does not
// complain: whoever configured no directory was already told once, at assembly
// time.
func (f FileStore) Save(value string) error {
	file, err := f.resolve()
	if err != nil || file == nil {
		return err
	}
	return file.Save(value)
}

// resolve settles the directory and the key.
//
// It returns (nil, nil) when there is no directory: the store being off is a
// normal state -- it is how the feature stays a shortcut rather than a
// requirement.
func (f FileStore) resolve() (*credentialFile, error) {
	if strings.TrimSpace(f.Name) == "" {
		return nil, fmt.Errorf("FileStore.Name is empty: it names the file, and it must " +
			"come from you rather than from the URL -- a URL carries secrets in its " +
			"query string, and a file name reaches logs, listings and backups")
	}
	if strings.ContainsAny(f.Name, `/\`) || f.Name == "." || f.Name == ".." {
		return nil, fmt.Errorf("FileStore.Name %q is a path, and it must be a plain name", f.Name)
	}

	dir := f.Dir
	if dir == "" {
		dir = os.Getenv(EnvCredentialDir)
	}
	if dir == "" {
		return nil, nil
	}

	key := f.Key
	if key == "" {
		key = os.Getenv(EnvCredentialKey)
	}
	env, err := NewCredentialBox(key)
	if err != nil {
		return nil, err
	}

	if err := prepareDirectory(dir); err != nil {
		return nil, err
	}

	return &credentialFile{path: filepath.Join(dir, f.Name+".cred"), env: env}, nil
}

// prepareDirectory creates the directory at 0700, and refuses one that
// already exists with looser permissions: a shared volume at 0777 is a public
// directory, and keeping a credential in it is no better than not keeping it.
func prepareDirectory(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("credential store: create %s: %w", dir, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("credential store: %s: %w", dir, err)
	case !info.IsDir():
		return fmt.Errorf("credential store: %s is not a directory", dir)
	}

	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("credential store: %s is mode %04o, and anyone on the host or "+
			"the shared volume can read what goes in it. Use 0700 -- `chmod 700 %s` on a "+
			"local directory, or mountOptions on the volume (for gcsfuse: "+
			"dir-mode=0700,file-mode=0600)", dir, mode, dir)
	}
	return nil
}

type credentialFile struct {
	path string
	env  CredentialBox
}

func (c *credentialFile) Describe() string { return c.path }

// Load returns the stored value, or "" when there is no usable one.
//
// An absent file is "there is no value", not an error: it is the very first
// run.
func (c *credentialFile) Load() (string, error) {
	raw, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("credential store: read %s: %w", c.path, err)
	}
	return c.env.Open(raw, c.path), nil
}

// Save writes the value, encrypted, atomically.
//
// A temporary file in the SAME directory plus a rename: a pod killed mid-write
// must not leave a half-written file behind, which would fail to decrypt and
// would send the next run to the seed in silence.
//
// Last writer wins, and that is a choice rather than an oversight: with the
// provider that motivated this feature it was checked that rotating does NOT
// invalidate the previous token, so two pods refreshing at the same time write
// two values that both work. For a provider that does invalidate the previous
// one, this does not hold -- and the warning is in Refresh.Store's doc.
func (c *credentialFile) Save(value string) error {
	payload, err := c.env.Seal(value)
	if err != nil {
		return err
	}

	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, ".cred-*")
	if err != nil {
		return fmt.Errorf("credential store: temp file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op when the rename worked

	// No Chmod: os.CreateTemp already creates at 0600, and an explicit chmod
	// would be a no-op on gcsfuse -- or an error, depending on the mount.
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credential store: write: %w", err)
	}
	// Sync before the rename: without it the rename can reach the disk before
	// the contents, and a crash leaves an empty file where a valid one was.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credential store: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credential store: close: %w", err)
	}

	if err := os.Rename(tmp.Name(), c.path); err != nil {
		return fmt.Errorf("credential store: rename into place: %w", err)
	}
	return nil
}
