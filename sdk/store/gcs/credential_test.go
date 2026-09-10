package gcs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// fakeGCS is a pretend GCS with what matters here: a per-object generation,
// and
// ifGenerationMatch de verdade.
type fakeGCS struct {
	mu         sync.Mutex
	conteudo   []byte
	generation int64
	existe     bool
	conflitos  int
}

func (g *fakeGCS) servidor(t *testing.T) *storage.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()

		// The client downloads the object with a GET on the XML path
		// (/bucket/object),
		// and not through the JSON API -- that is how the first version of this
		// fake
		// errou.
		if r.Method == http.MethodGet {
			if !g.existe {
				http.Error(w, `{"error":{"code":404}}`, http.StatusNotFound)
				return
			}
			w.Header().Set("x-goog-generation", fmt.Sprint(g.generation))
			w.Header().Set("Content-Length", fmt.Sprint(len(g.conteudo)))
			_, _ = w.Write(g.conteudo)
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload/") {
			q := r.URL.Query()
			if v := q.Get("ifGenerationMatch"); v != "" {
				expected := v == fmt.Sprint(g.generation)
				if v == "0" {
					expected = !g.existe
				}
				if !expected {
					g.conflitos++
					http.Error(w, `{"error":{"code":412,"message":"generation mismatch"}}`,
						http.StatusPreconditionFailed)
					return
				}
			}
			body, _ := io.ReadAll(r.Body)
			g.conteudo = extrairCorpoMultipart(body)
			g.generation++
			g.existe = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "obj", "bucket": "b", "generation": fmt.Sprint(g.generation),
			})
			return
		}
		http.Error(w, "nao esperado: "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	cli, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// extrairCorpoMultipart pega a segunda parte do upload multipart do GCS.
func extrairCorpoMultipart(b []byte) []byte {
	partes := strings.Split(string(b), "\r\n\r\n")
	if len(partes) < 3 {
		return b
	}
	body := partes[2]
	if i := strings.LastIndex(body, "\r\n--"); i >= 0 {
		body = body[:i]
	}
	return []byte(body)
}

func credential(t *testing.T, g *fakeGCS) Credential {
	t.Helper()
	t.Setenv(core.EnvCredentialKey, "")
	generations.Delete("gs://b/obj")
	return Credential{Bucket: "b", Object: "obj", Client: g.servidor(t)}
}

// TestItStoresAndReturns: o caminho feliz.
func TestItStoresAndReturns(t *testing.T) {
	g := &fakeGCS{}
	c := credential(t, g)

	if v, err := c.Load(); err != nil || v != "" {
		t.Fatalf("objeto ausente = (%q, %v); devia ser vazio sem erro", v, err)
	}
	if err := c.Save("session=abc=="); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := c.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "session=abc==" {
		t.Errorf("Load = %q", got)
	}
}

// TestTheConditionalWrite: whoever read generation 1 and tries to write after
// somebody else wrote 2 gets a 412 -- and does NOT overwrite. That is the
// difference between a real CAS and last-writer-wins, which is what a volume
// would allow.
func TestTheConditionalWrite(t *testing.T) {
	g := &fakeGCS{}
	c := credential(t, g)

	if err := c.Save("primeiro"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(); err != nil { // lembra a geracao 1
		t.Fatal(err)
	}

	// Another process writes from outside, advancing the generation.
	g.mu.Lock()
	g.generation++
	g.conteudo = []byte("brevis-cred/1p\nde-outro-processo")
	g.mu.Unlock()

	// This run's write was conditional on the generation it read.
	if err := c.Save("meu-valor"); err != nil {
		t.Fatalf("o conflito virou erro: %v", err)
	}
	g.mu.Lock()
	conflitos, conteudo := g.conflitos, string(g.conteudo)
	g.mu.Unlock()

	if conflitos != 1 {
		t.Errorf("conflitos = %d, esperado 1 -- a escrita nao foi condicional", conflitos)
	}
	if !strings.Contains(conteudo, "de-outro-processo") {
		t.Errorf("sobrescreveu o valor mais novo: %q", conteudo)
	}
}

// TestTheFirstWriteUsesDoesNotExist: without it, two first runs
// simultaneous ones would both write, and the older could arrive last.
func TestTheFirstWriteUsesDoesNotExist(t *testing.T) {
	g := &fakeGCS{}
	c := credential(t, g)

	if _, err := c.Load(); err != nil { // objeto ausente, geracao 0
		t.Fatal(err)
	}
	// Another process creates the object first.
	g.mu.Lock()
	g.existe, g.generation, g.conteudo = true, 7, []byte("brevis-cred/1p\nde-outro")
	g.mu.Unlock()

	if err := c.Save("meu"); err != nil {
		t.Fatalf("o conflito virou erro: %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conflitos != 1 {
		t.Errorf("conflitos = %d; a primeira gravacao nao usou DoesNotExist", g.conflitos)
	}
	if !strings.Contains(string(g.conteudo), "de-outro") {
		t.Errorf("sobrescreveu: %q", g.conteudo)
	}
}

// TestBucketAndObjectAreRequired.
func TestBucketAndObjectAreRequired(t *testing.T) {
	for _, c := range []Credential{{Object: "o"}, {Bucket: "b"}, {}} {
		if err := c.CheckStore(); err == nil {
			t.Errorf("aceitou %+v", c)
		}
	}
}

// TestDescribeRevealsNothing: Describe reaches the log.
func TestDescribeRevealsNothing(t *testing.T) {
	c := Credential{Bucket: "b", Object: "o", Key: "chave-secreta-aqui"}
	if strings.Contains(c.Describe(), "chave-secreta") {
		t.Errorf("Describe vaza a chave: %q", c.Describe())
	}
	if c.Describe() != "gs://b/o" {
		t.Errorf("Describe = %q", c.Describe())
	}
}
