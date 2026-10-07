package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// ErrNotExist says an object is not there, told apart from every other reason a
// read can fail.
//
// The Store interface had no word for it. Each backend returned its own -- GCS
// `storage.ErrObjectNotExist`, S3 a `NoSuchKey`, the filesystem `fs.ErrNotExist`
// -- and a caller that wanted to distinguish "nobody wrote this yet" from "the
// bucket is unreachable" had to import both cloud SDKs to ask. Which is the one
// thing this package's design exists to avoid.
//
// Every Store wraps it, so `errors.Is(err, core.ErrNotExist)` is the question,
// and the message still names the object.
var ErrNotExist = errors.New("object does not exist")

// LocalStore is the filesystem as a Store.
//
// `from.Files` and `to.Files` treat a nil Store as the local disk and handle it
// inline, which is right for them -- a CSV on disk should compile no backend at
// all. But `persist` needs the same three verbs over whichever place the
// installation chose, and "the local disk" is one of those places: it is what
// makes `brevis run` and a test work with no cloud account.
//
// Bucket is the first path segment of an absolute path, so a "bucket" of "" and
// a key of "/tmp/ctx/k.json" address the file directly.
type LocalStore struct{}

func (LocalStore) Scheme() string { return "" }

func (LocalStore) List(_ context.Context, bucket, prefix string) ([]string, error) {
	dir := filepath.Join("/"+bucket, prefix)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("listing %s: %w", dir, ErrNotExist)
		}
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() {
			keys = append(keys, filepath.Join(prefix, e.Name()))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (LocalStore) Open(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	p := filepath.Join("/"+bucket, key)
	f, err := os.Open(p) //nolint:gosec // the path is the installation's own
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("opening %s: %w", p, ErrNotExist)
		}
		return nil, fmt.Errorf("opening %s: %w", p, err)
	}
	return f, nil
}

// Create writes through a temporary file and renames, which is what makes a
// local write atomic: on one filesystem a rename cannot be seen half done, so a
// concurrent reader gets the old value or the new one and never a torn one.
// The same property object storage gets from a single PUT.
func (LocalStore) Create(_ context.Context, bucket, key string, r io.Reader) error {
	p := filepath.Join("/"+bucket, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(p), ".brevis-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", p, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", p, err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("renaming into %s: %w", p, err)
	}
	return nil
}
