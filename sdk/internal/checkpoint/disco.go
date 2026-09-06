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

// discoLocal serves core.Store's three verbs on the filesystem, so the
// checkpoint does not need two code paths.
//
// A local `At` is not only a testing convenience: it is what makes the
// checkpoint work under a local executor and in a pod with a mounted volume,
// which is where it costs least and serves just as well.
type discoLocal struct{}

func (discoLocal) Scheme() string { return "" }

// List returns the directory's files, sorted. It does not descend into
// subdirectories: a checkpoint's parts are all siblings, and descending would
// bring in another checkpoint's files.
func (discoLocal) List(_ context.Context, _, prefixo string) ([]string, error) {
	entradas, err := os.ReadDir(prefixo)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // a depot that does not exist yet is not an error
	}
	if err != nil {
		return nil, err
	}
	var chaves []string
	for _, e := range entradas {
		if !e.IsDir() {
			chaves = append(chaves, filepath.Join(prefixo, e.Name()))
		}
	}
	sort.Strings(chaves)
	return chaves, nil
}

func (discoLocal) Open(_ context.Context, _, chave string) (io.ReadCloser, error) {
	return os.Open(filepath.Clean(chave))
}

// Create writes to a temporary file and renames. On the same filesystem the
// rename is atomic, so a part that exists is a whole part -- which is Conferir's
// premise.
func (discoLocal) Create(_ context.Context, _, chave string, r io.Reader) error {
	dir := filepath.Dir(chave)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("criando %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".brevis-cp-*")
	if err != nil {
		return fmt.Errorf("criando um temporario em %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gravando %s: %w", chave, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fechando %s: %w", chave, err)
	}
	if err := os.Rename(tmp.Name(), chave); err != nil {
		return fmt.Errorf("renomeando para %s: %w", chave, err)
	}
	return nil
}
