package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Credential keeps the rotated credential in a GCS object.
//
// It is what makes the refresh hold between runs: with no store, the renewed
// value dies with the process, and somebody re-pastes the seed once per window
// forever. With it, the environment variable stops holding the ROTATING value
// and starts holding the seed, pasted once.
//
//	Refresh: &from.Refresh{
//	    URL:       "https://api.example.com/auth/session",
//	    ExpiresAt: from.JSONField("expires"),
//	    Store:     gcs.Credential{Bucket: "my-project-credentials", Object: "app-session"},
//	}
//
// The object holds the credential and nothing beyond it: no `expires`, no who,
// no when -- the object's metadata already says the when, and the rest goes
// stale.
//
// Importing this package costs the Google Storage client. A fetcher using
// from.FileStore never compiles it -- the same rule as core.Store.
type Credential struct {
	// Bucket and Object say where. Both required.
	//
	// The object's name comes from you and NEVER from the URL: a URL carries
	// secrets in its query string, and an object name leaks into logs and
	// listings.
	Bucket string
	Object string

	// Key encrypts the contents with AES-256-GCM. Optional; empty falls back to
	// BREVIS_CREDENTIAL_KEY, and empty in both writes in the clear, saying once
	// in the log that it is in the clear.
	//
	// On a dedicated bucket, with IAM for a single service account and public
	// access blocked, the key protects little: it lives in the same secret the
	// tasks do, so whoever reads the bucket has it too. The control is the
	// bucket's.
	Key string

	// Client is the Storage client. Nil creates one per run from the
	// environment's credentials -- which in a pod with Workload Identity is all
	// that is needed.
	Client *storage.Client
}

// CheckStore satisfies core.CredentialStoreChecker: it refuses at assembly
// time.
func (c Credential) CheckStore() error {
	if c.Bucket == "" || c.Object == "" {
		return fmt.Errorf("gcs.Credential needs Bucket and Object")
	}
	env, err := core.NewCredentialBox(c.key())
	if err != nil {
		return err
	}
	core.WarnIfPlaintext(env, c.Describe())
	return nil
}

// Describe names the object, never its contents.
func (c Credential) Describe() string { return "gs://" + c.Bucket + "/" + c.Object }

func (c Credential) key() string {
	if c.Key != "" {
		return c.Key
	}
	return os.Getenv(core.EnvCredentialKey)
}

// generations remembers the generation read per object, for the conditional
// write.
//
// It lives in the process because that is exactly the scope of what is being
// remembered: "the generation I read". The coordination between processes is
// ifGenerationMatch itself, on the server.
var generations sync.Map

// Load returns the stored credential, and remembers the generation for Save.
//
// An absent object is "there is no value", not an error: it is the very first
// run, and the caller falls back to the seed.
func (c Credential) Load() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, release, err := c.client(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	obj := cli.Bucket(c.Bucket).Object(c.Object)
	r, err := obj.NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		// Zero means "it did not exist when I read", and Save becomes
		// DoesNotExist -- which is the right condition for the first write.
		generations.Store(c.Describe(), int64(0))
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("credential store: reading %s: %w", c.Describe(), err)
	}
	defer func() { _ = r.Close() }()

	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", fmt.Errorf("credential store: reading %s: %w", c.Describe(), err)
	}
	generations.Store(c.Describe(), r.Attrs.Generation)

	env, err := core.NewCredentialBox(c.key())
	if err != nil {
		return "", err
	}
	return env.Open(raw, c.Describe()), nil
}

// Save writes the credential, conditional on the generation Load read.
//
// If another process wrote in between, GCS returns 412 and the write does NOT
// happen -- instead of overwriting. That is a real compare-and-swap, with no
// lock, and it is the reason this store exists rather than a file on a volume:
// `rename` on gcsfuse is not atomic, and only last-writer-wins would fit there.
//
// Losing the 412 is not an error. The other process refreshed too, its value is
// valid too, and this run's own keeps working until the run ends. What must not
// happen is the older one arriving last and erasing the newer.
func (c Credential) Save(value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, release, err := c.client(ctx)
	if err != nil {
		return err
	}
	defer release()

	env, err := core.NewCredentialBox(c.key())
	if err != nil {
		return err
	}
	payload, err := env.Seal(value)
	if err != nil {
		return err
	}

	cond := storage.Conditions{DoesNotExist: true}
	if g, ok := generations.Load(c.Describe()); ok {
		if gen := g.(int64); gen != 0 {
			cond = storage.Conditions{GenerationMatch: gen}
		}
	}

	w := cli.Bucket(c.Bucket).Object(c.Object).If(cond).NewWriter(ctx)
	// No cache: a credential object served from a cache would be an old
	// version read as if it were the current one.
	w.CacheControl = "no-store"
	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		return c.writeError(err)
	}
	if err := w.Close(); err != nil {
		return c.writeError(err)
	}
	return nil
}

// writeError turns the generation conflict into "I did not write, and that is
// fine", and lets everything else be an error.
func (c Credential) writeError(err error) error {
	var api *googleapi.Error
	if errors.As(err, &api) && (api.Code == http.StatusPreconditionFailed || api.Code == http.StatusConflict) {
		slog.Info("credential store: another process rotated first, keeping theirs",
			"store", c.Describe(),
			"why", "the write was conditional on the generation this run read")
		return nil
	}
	return fmt.Errorf("credential store: writing %s: %w", c.Describe(), err)
}

// client returns the client and how to release it. A client the caller passed
// in is the caller's, and is not closed here.
func (c Credential) client(ctx context.Context) (*storage.Client, func(), error) {
	if c.Client != nil {
		return c.Client, func() {}, nil
	}
	cli, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("credential store: google storage client: %w", err)
	}
	return cli, func() { _ = cli.Close() }, nil
}
