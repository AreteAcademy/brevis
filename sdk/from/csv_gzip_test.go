package from_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"

	"github.com/AreteAcademy/brevis/sdk/from"
)

func csvPontoEVirgula() []byte {
	return []byte("municipio;populacao\nSão Paulo;11451999\nRio;6211423\n")
}

func lerTudo(t *testing.T, r core.Reader) []map[string]any {
	t.Helper()
	lines, err := r.Read(context.Background(), core.ReadOptions{Stats: &core.Stats{}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out []map[string]any
	for env, err := range lines {
		if err != nil {
			t.Fatalf("linha: %v", err)
		}
		out = append(out, asMap(t, env.Payload))
	}
	return out
}

// `;` is the de facto standard across much of Europe and in the open-data
// portals. Without this option, the way out was decoding the CSV by hand -- that
// is, reimplementing csv.Reader to change one character.
func TestHTTPWithASemicolonDelimiter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(csvPontoEVirgula())
	}))
	defer srv.Close()

	lines := lerTudo(t, from.HTTP{URL: srv.URL, Format: core.FormatCSV, Delimiter: ';'})
	if len(lines) != 2 {
		t.Fatalf("saiu com %d linhas: %v", len(lines), lines)
	}
	if lines[0]["municipio"] != "São Paulo" || lines[0]["populacao"] != "11451999" {
		t.Errorf("primeira linha: %v", lines[0])
	}
}

// Without the delimiter, the whole line becomes ONE column whose name is the
// whole header -- the defect the field exists to prevent.
func TestWithoutADelimiterASemicolonCSVBecomesOneColumn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(csvPontoEVirgula())
	}))
	defer srv.Close()

	lines := lerTudo(t, from.HTTP{URL: srv.URL, Format: core.FormatCSV})
	if len(lines[0]) != 1 {
		t.Errorf("com vírgula o CSV devia virar uma coluna só, veio %v", lines[0])
	}
}

// A `.csv.gz` served as CONTENT is how nearly every open-data portal publishes a
// large file. from.Files already decompressed by extension; the rule existed in
// the SDK and simply did not reach HTTP.
func TestHTTPDecompressesGzip(t *testing.T) {
	var comprimido bytes.Buffer
	gz := gzip.NewWriter(&comprimido)
	_, _ = gz.Write(csvPontoEVirgula())
	_ = gz.Close()

	for _, caso := range []struct {
		name, contentType, path string
	}{
		{"por content-type", "application/gzip", "/dados"},
		{"por extensão", "application/octet-stream", "/dados.csv.gz"},
		{"por content-encoding", "", "/dados"},
	} {
		t.Run(caso.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if caso.contentType != "" {
					w.Header().Set("Content-Type", caso.contentType)
				} else {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = w.Write(comprimido.Bytes())
			}))
			defer srv.Close()

			lines := lerTudo(t, from.HTTP{
				URL: srv.URL + caso.path, Format: core.FormatCSV, Delimiter: ';',
			})
			if len(lines) != 2 || lines[1]["municipio"] != "Rio" {
				t.Errorf("não descomprimiu: %v", lines)
			}
		})
	}
}

// A response that announces gzip and is not fails saying so, and not as a
// decoding error about invalid CSV.
func TestALyingGzipFailsSayingWhatItIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write([]byte("isto nao e gzip"))
	}))
	defer srv.Close()

	_, err := from.HTTP{URL: srv.URL, Format: core.FormatCSV}.Read(
		context.Background(), core.ReadOptions{Stats: &core.Stats{}})
	if err == nil {
		t.Fatal("um corpo que não é gzip passou")
	}
	if !containsAll(err.Error(), "gzip") {
		t.Errorf("a mensagem não diz o que houve: %v", err)
	}
}

// The same delimiter holds for files, which is where a `;` CSV usually
// vir depois de baixado.
func TestFilesWithADelimiter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dados.csv")
	if err := os.WriteFile(path, csvPontoEVirgula(), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := lerTudo(t, from.Files{Path: path, Format: core.FormatCSV, Delimiter: ';'})
	if len(lines) != 2 || lines[0]["municipio"] != "São Paulo" {
		t.Errorf("linhas: %v", lines)
	}
}

// comoMapa narrows the payload. Every source yields the same record model, so
// there is nothing to normalise -- this only fails loudly if that stops being
// true.
func asMap(t *testing.T, p any) map[string]any {
	t.Helper()
	m, ok := p.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload: %T", p)
	}
	return m
}

func containsAll(s string, partes ...string) bool {
	for _, p := range partes {
		if !bytes.Contains([]byte(s), []byte(p)) {
			return false
		}
	}
	return true
}
