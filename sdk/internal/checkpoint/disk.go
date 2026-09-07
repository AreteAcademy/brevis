package checkpoint

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

// localDisk serves core.Store's three verbs on the filesystem, so the
// checkpoint does not need two code paths.
//
// A local `At` is not only a testing convenience: it is what makes the
// checkpoint work under a local executor and in a pod with a mounted volume,
// which is where it costs least and serves just as well.
type localDisk struct{}

func (localDisk) Scheme() string { return "" }

// List returns the directory's files, sorted. It does not descend into
// subdirectories: a checkpoint's parts are all siblings, and descending would
// bring in another checkpoint's files.
func (localDisk) List(_ context.Context, _, prefix string) ([]string, error) {
	entries, err := os.ReadDir(prefix)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // a depot that does not exist yet is not an error
	}
	if err != nil {
		return nil, err
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

func (localDisk) Open(_ context.Context, _, key string) (io.ReadCloser, error) {
	return os.Open(filepath.Clean(key))
}

// Create writes to a temporary file and renames. On the same filesystem the
// rename is atomic, so a part that exists is a whole part -- which is Check's
// premise.
func (localDisk) Create(_ context.Context, _, key string, r io.Reader) error {
	dir := filepath.Dir(key)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".brevis-cp-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), key); err != nil {
		return fmt.Errorf("renaming into place as %s: %w", key, err)
	}
	return nil
}
