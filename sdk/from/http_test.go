package from

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// from.HTTP is an adapter: it copies its fields into the core.Source extract
// consumes. A field forgotten in that copy breaks nothing that compiles -- it
// simply stops having an effect, which is the defect this SDK has found in
// itself most often.
//
// This test checks that every field reaches the other side.
func TestHTTPPassesEveryFieldThrough(t *testing.T) {
	var (
		metodo string
		body   string
		header string
		path   string
		calls  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		metodo = r.Method
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		header = r.Header.Get("X-Cliente")
		path = r.URL.Path
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	stats := &core.Stats{}
	var preview strings.Builder

	seq, err := HTTP{
		URL:          srv.URL + "/v1/eventos",
		Method:       http.MethodPost,
		Body:         strings.NewReader(`{"q":1}`),
		Header:       map[string][]string{"X-Cliente": {"brevis"}},
		Timeout:      5 * time.Second,
		TotalTimeout: time.Minute,
		Format:       core.FormatJSON,
		MaxPages:     3,
		RetryConfig:  &core.RetryConfig{MaxAttempts: 1},
	}.Read(context.Background(), core.ReadOptions{
		Stats: stats, Preview: 5, PreviewWriter: &preview,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		n++
	}

	// Os campos do driver.
	if metodo != http.MethodPost {
		t.Errorf("Method não chegou: %q", metodo)
	}
	if body != `{"q":1}` {
		t.Errorf("Body não chegou: %q", body)
	}
	if header != "brevis" {
		t.Errorf("Header não chegou: %q", header)
	}
	if path != "/v1/eventos" {
		t.Errorf("URL não chegou inteira: %q", path)
	}

	// The options that cross every driver.
	if stats.Bytes == 0 || stats.Pages == 0 {
		t.Errorf("Stats não foi preenchido: %+v", stats)
	}
	if !strings.Contains(preview.String(), "id") {
		t.Errorf("o Preview não saiu:\n%s", preview.String())
	}
	if n != 1 {
		t.Errorf("%d registros, esperado 1", n)
	}
}

// Every format has to reach the right decoder. An ignored Format would decode
// JSON where the fetcher asked for CSV.
func TestHTTPForwardsEachFormat(t *testing.T) {
	casos := []struct {
		formato core.Format
		body    string
		field   string
	}{
		{core.FormatJSON, `[{"a":"1"}]`, "a"},
		{core.FormatNDJSON, `{"a":"1"}` + "\n", "a"},
		{core.FormatCSV, "a\n1\n", "a"},
		{core.FormatXML, `<r><item><a>1</a></item></r>`, "a"},
	}

	for _, c := range casos {
		t.Run(string(c.formato), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			seq, err := HTTP{URL: srv.URL, Format: c.formato}.
				Read(context.Background(), core.ReadOptions{})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			n := 0
			for env, err := range seq {
				if err != nil {
					t.Fatalf("iterando: %v", err)
				}
				if _, err := core.AsObject(env.Payload); err != nil {
					t.Errorf("o registro não é objeto: %v", env.Payload)
				}
				n++
			}
			if n == 0 {
				t.Errorf("nenhum registro saiu de um corpo %s válido", c.formato)
			}
		})
	}
}

func TestHTTPRefusesAnUnknownFormat(t *testing.T) {
	_, err := HTTP{URL: "http://x", Format: "yaml"}.Read(context.Background(), core.ReadOptions{})
	if err == nil {
		t.Fatal("um formato que o SDK não decodifica tem de ser recusado")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("o erro precisa nomear o formato: %v", err)
	}
}

// Describe is what appears in the log and in the error message, so it must not
// carry the secret the URL carries.
func TestHTTPDescribeLeaksNoSecret(t *testing.T) {
	got := HTTP{URL: "https://api.exemplo.com/v1?api_key=SEGREDO&lat=-23.5"}.Describe()

	if strings.Contains(got, "SEGREDO") {
		t.Errorf("Describe vazou a chave: %s", got)
	}
	if !strings.Contains(got, "api.exemplo.com") || !strings.Contains(got, "lat=-23.5") {
		t.Errorf("Describe apagou o que serve para depurar: %s", got)
	}
}

// A Stream hands its records over as it reads them, and they arrive in the
// order it emitted them -- counted, previewed, and with the response whole
// (#140). The body is not one the SDK decodes: the function is what reads it.
func TestAStreamHandsItsRecordsOverInOrder(t *testing.T) {
	const body = "alpha\nbeta\ngamma"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Vendor", "acme")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var seen core.Response
	stats := &core.Stats{}
	var preview strings.Builder
	seq, err := HTTP{
		URL:         srv.URL + "/v1/sheet",
		RetryConfig: &core.RetryConfig{MaxAttempts: 1},
		Stream: func(r core.Response, emit func(any) error) error {
			seen = r
			for _, line := range strings.Split(string(r.Bytes()), "\n") {
				if err := emit(map[string]any{"line": line}); err != nil {
					return err
				}
			}
			return nil
		},
	}.Read(context.Background(), core.ReadOptions{
		Stats: stats, Preview: 5, PreviewWriter: &preview,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var got []string
	for env, err := range seq {
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		got = append(got, env.Payload.(map[string]any)["line"].(string))
	}

	if strings.Join(got, ",") != "alpha,beta,gamma" {
		t.Errorf("records = %v, want alpha, beta, gamma in that order", got)
	}
	if string(seen.Bytes()) != body || seen.Status != http.StatusOK ||
		seen.Header.Get("X-Vendor") != "acme" || !strings.HasSuffix(seen.URL, "/v1/sheet") {
		t.Errorf("the Response did not arrive whole: status %d, header %v, url %q, body %q",
			seen.Status, seen.Header, seen.URL, seen.Bytes())
	}
	if stats.Pages != 1 || stats.Bytes != int64(len(body)) {
		t.Errorf("Stats = %+v, want 1 page and %d bytes", stats, len(body))
	}
	if !strings.Contains(preview.String(), "gamma") || !strings.Contains(preview.String(), "3 row") {
		t.Errorf("the preview does not show the three records:\n%s", preview.String())
	}
}

// Records and Stream both say what a response means, and one of them would
// be set and do nothing. Refused before any request, naming which to keep.
func TestAStreamWithRecordsIsRefusedBeforeAnyRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()

	_, err := HTTP{
		URL:     srv.URL,
		Records: func(core.Response) ([]any, error) { return nil, nil },
		Stream:  func(core.Response, func(any) error) error { return nil },
	}.Read(context.Background(), core.ReadOptions{})
	if err == nil {
		t.Fatal("Records and Stream together were accepted; one of them would be ignored")
	}
	if !strings.Contains(err.Error(), "Records") || !strings.Contains(err.Error(), "Stream") {
		t.Errorf("the error does not name the two fields: %v", err)
	}
	if calls != 0 {
		t.Errorf("%d request(s) went out before the refusal", calls)
	}
}
