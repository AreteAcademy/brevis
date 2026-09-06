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
	linhas, err := r.Read(context.Background(), core.ReadOptions{Stats: &core.Stats{}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out []map[string]any
	for env, err := range linhas {
		if err != nil {
			t.Fatalf("linha: %v", err)
		}
		out = append(out, comoMapa(t, env.Payload))
	}
	return out
}

// `;` é o padrão de fato em boa parte da Europa e nos portais de dados
// abertos. Sem esta opção, a saída era decodificar o CSV à mão -- ou seja,
// reimplementar o csv.Reader para trocar um caractere.
func TestHTTPComDelimitadorPontoEVirgula(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(csvPontoEVirgula())
	}))
	defer srv.Close()

	linhas := lerTudo(t, from.HTTP{URL: srv.URL, Format: core.FormatCSV, Delimitador: ';'})
	if len(linhas) != 2 {
		t.Fatalf("saiu com %d linhas: %v", len(linhas), linhas)
	}
	if linhas[0]["municipio"] != "São Paulo" || linhas[0]["populacao"] != "11451999" {
		t.Errorf("primeira linha: %v", linhas[0])
	}
}

// Sem o delimitador, a linha inteira vira UMA coluna cujo nome é o cabeçalho
// inteiro -- o defeito que o campo existe para evitar.
func TestSemDelimitadorOCSVComPontoEVirgulaViraUmaColunaSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(csvPontoEVirgula())
	}))
	defer srv.Close()

	linhas := lerTudo(t, from.HTTP{URL: srv.URL, Format: core.FormatCSV})
	if len(linhas[0]) != 1 {
		t.Errorf("com vírgula o CSV devia virar uma coluna só, veio %v", linhas[0])
	}
}

// Um `.csv.gz` servido como CONTEÚDO é como quase todo portal de dados abertos
// publica arquivo grande. O from.Files já descomprimia pela extensão; a regra
// existia no SDK e só não alcançava o HTTP.
func TestHTTPDescomprimeGzip(t *testing.T) {
	var comprimido bytes.Buffer
	gz := gzip.NewWriter(&comprimido)
	_, _ = gz.Write(csvPontoEVirgula())
	_ = gz.Close()

	for _, caso := range []struct {
		nome, contentType, caminho string
	}{
		{"por content-type", "application/gzip", "/dados"},
		{"por extensão", "application/octet-stream", "/dados.csv.gz"},
		{"por content-encoding", "", "/dados"},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if caso.contentType != "" {
					w.Header().Set("Content-Type", caso.contentType)
				} else {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = w.Write(comprimido.Bytes())
			}))
			defer srv.Close()

			linhas := lerTudo(t, from.HTTP{
				URL: srv.URL + caso.caminho, Format: core.FormatCSV, Delimitador: ';',
			})
			if len(linhas) != 2 || linhas[1]["municipio"] != "Rio" {
				t.Errorf("não descomprimiu: %v", linhas)
			}
		})
	}
}

// Uma resposta que se anuncia gzip e não é falha nomeando isso, e não como um
// erro de decodificação sobre CSV inválido.
func TestGzipMentirosoFalhaDizendoOQueE(t *testing.T) {
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
	if !contemTudo(err.Error(), "gzip") {
		t.Errorf("a mensagem não diz o que houve: %v", err)
	}
}

// O mesmo delimitador vale para arquivos, que é de onde o CSV com `;` costuma
// vir depois de baixado.
func TestFilesComDelimitador(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "dados.csv")
	if err := os.WriteFile(caminho, csvPontoEVirgula(), 0o600); err != nil {
		t.Fatal(err)
	}
	linhas := lerTudo(t, from.Files{Path: caminho, Format: core.FormatCSV, Delimitador: ';'})
	if len(linhas) != 2 || linhas[0]["municipio"] != "São Paulo" {
		t.Errorf("linhas: %v", linhas)
	}
}

// comoMapa normaliza o payload: o decoder de CSV entrega map[string]string, o
// de JSON entrega map[string]any.
func comoMapa(t *testing.T, p any) map[string]any {
	t.Helper()
	switch m := p.(type) {
	case map[string]any:
		return m
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	t.Fatalf("payload inesperado: %T", p)
	return nil
}

func contemTudo(s string, partes ...string) bool {
	for _, p := range partes {
		if !bytes.Contains([]byte(s), []byte(p)) {
			return false
		}
	}
	return true
}
