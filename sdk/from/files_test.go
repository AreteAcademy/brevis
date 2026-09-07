package from

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

func escreve(t *testing.T, dir, name, conteudo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
}

func le(t *testing.T, f Files) ([]core.Envelope, error) {
	t.Helper()
	seq, err := f.Read(context.Background(), core.ReadOptions{})
	if err != nil {
		return nil, err
	}
	var out []core.Envelope
	for env, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, env)
	}
	return out, nil
}

func TestFilesLeNDJSONLocal(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "a.ndjson", "{\"id\":1}\n{\"id\":2}\n")

	got, err := le(t, Files{Path: filepath.Join(dir, "*.ndjson")})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d registros, esperado 2", len(got))
	}
}

// The order is a contract: a positional Key depends on it, and without it the
// same record's ingestion_id changes between runs.
func TestFilesLeEmOrdemDeterminística(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "c.ndjson", `{"n":"c"}`+"\n")
	escreve(t, dir, "a.ndjson", `{"n":"a"}`+"\n")
	escreve(t, dir, "b.ndjson", `{"n":"b"}`+"\n")

	for i := 0; i < 5; i++ {
		got, err := le(t, Files{Path: filepath.Join(dir, "*.ndjson")})
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, e := range got {
			order = append(order, e.Payload.(map[string]any)["n"].(string))
		}
		if strings.Join(order, "") != "abc" {
			t.Fatalf("ordem = %v, esperado a,b,c em toda execução", order)
		}
	}
}

func TestFilesReadsCSVWithAHeader(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "p.csv", "nome,idade\nana,30\nbeto,41\n")

	got, err := le(t, Files{Path: filepath.Join(dir, "*.csv"), Format: core.FormatCSV})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d registros, esperado 2", len(got))
	}
	// The CSV decoder yields map[string]any -- the same record model as every
	// other source. It used to yield map[string]string, and that meant no
	// transformer worked on a CSV at all.
	if got[0].Payload.(map[string]any)["nome"] != "ana" {
		t.Errorf("a primeira linha não virou registro: %v", got[0].Payload)
	}
}

func TestFilesDecompressesGzipByExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.ndjson.gz"), gzipado(t, "{\"id\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := le(t, Files{Path: filepath.Join(dir, "*.gz")})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d registros, esperado 1", len(got))
	}
}

// A .gz that is not gzip has to fail naming the file, not as "invalid JSON"
// further down.
func TestFilesInvalidGzipNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "mentira.ndjson.gz", "isto nao e gzip")

	_, err := le(t, Files{Path: filepath.Join(dir, "*.gz")})
	if err == nil {
		t.Fatal("um .gz que não é gzip tem de falhar")
	}
	if !strings.Contains(err.Error(), "mentira.ndjson.gz") || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("o erro precisa nomear o arquivo e o problema: %v", err)
	}
}

// An empty directory is a result, not a failure -- for the same reason a 204 is
// not.
func TestFilesAnEmptyDirectoryIsNotAFailure(t *testing.T) {
	got, err := le(t, Files{Path: filepath.Join(t.TempDir(), "*.ndjson")})
	if err != nil {
		t.Fatalf("um diretório sem arquivos é uma janela vazia: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d registros de um diretório vazio", len(got))
	}
}

// The path and the backend have to match, and the error names both.
func TestFilesRefusesAPathWithNoStore(t *testing.T) {
	_, err := Files{Path: "s3://bucket/x/*.ndjson"}.Read(context.Background(), core.ReadOptions{})
	if err == nil {
		t.Fatal("um caminho s3:// sem Store não tem como ser lido")
	}
	for _, quer := range []string{"s3", "Store"} {
		if !strings.Contains(err.Error(), quer) {
			t.Errorf("o erro não menciona %q: %v", quer, err)
		}
	}
}

func TestFilesRefusesAStoreForAnotherScheme(t *testing.T) {
	_, err := Files{Path: "gs://b/x/*.ndjson", Store: falso{"s3"}}.
		Read(context.Background(), core.ReadOptions{})
	if err == nil {
		t.Fatal("um Store de S3 não serve um caminho gs://")
	}
	if !strings.Contains(err.Error(), "gs") || !strings.Contains(err.Error(), "s3") {
		t.Errorf("o erro precisa nomear os dois lados: %v", err)
	}
}

func TestFilesCountsTheBytesRead(t *testing.T) {
	dir := t.TempDir()
	body := "{\"id\":1}\n{\"id\":2}\n"
	escreve(t, dir, "a.ndjson", body)

	stats := &core.Stats{}
	seq, err := Files{Path: filepath.Join(dir, "*.ndjson")}.
		Read(context.Background(), core.ReadOptions{Stats: stats})
	if err != nil {
		t.Fatal(err)
	}
	for range seq {
	}

	if stats.Bytes != int64(len(body)) {
		t.Errorf("Stats.Bytes = %d, esperado %d", stats.Bytes, len(body))
	}
	if stats.Pages != 1 {
		t.Errorf("Stats.Pages = %d; um arquivo é uma página", stats.Pages)
	}
}

// The preview holds for every driver, not only for HTTP -- otherwise
// ReadOptions.Preview would be a dead field here.
func TestFilesHonraOPreview(t *testing.T) {
	dir := t.TempDir()
	escreve(t, dir, "a.ndjson", "{\"id\":1,\"n\":\"alfa\"}\n{\"id\":2,\"n\":\"beta\"}\n")

	var out strings.Builder
	seq, err := Files{Path: filepath.Join(dir, "*.ndjson")}.Read(context.Background(),
		core.ReadOptions{Preview: 5, PreviewWriter: &out})
	if err != nil {
		t.Fatal(err)
	}
	for range seq {
	}

	if !strings.Contains(out.String(), "alfa") || !strings.Contains(out.String(), "2 rows") {
		t.Errorf("o preview não saiu:\n%s", out.String())
	}
}

type falso struct{ esquema string }

func (f falso) Scheme() string                                              { return f.esquema }
func (f falso) List(context.Context, string, string) ([]string, error)      { return nil, nil }
func (f falso) Open(context.Context, string, string) (io.ReadCloser, error) { return nil, nil }
func (f falso) Create(context.Context, string, string, io.Reader) error     { return nil }

func gzipado(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
