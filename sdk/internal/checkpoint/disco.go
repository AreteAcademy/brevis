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

// discoLocal atende os tres verbos do core.Store no sistema de arquivos, para
// o checkpoint nao precisar de dois caminhos de codigo.
//
// Um `At` local nao e so conveniencia de teste: e o que faz o checkpoint
// funcionar num executor local e num pod com volume montado, que e onde ele
// custa menos e serve igual.
type discoLocal struct{}

func (discoLocal) Scheme() string { return "" }

// List devolve os arquivos do diretorio, ordenados. Nao desce em subdiretorio:
// as partes de um checkpoint sao todas irmas, e descer traria arquivo de outro.
func (discoLocal) List(_ context.Context, _, prefixo string) ([]string, error) {
	entradas, err := os.ReadDir(prefixo)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // deposito que ainda nao existe nao e erro
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

// Create escreve num temporario e renomeia. No mesmo sistema de arquivos o
// rename e atomico, entao uma parte que existe e uma parte inteira -- que e a
// premissa de Conferir.
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
