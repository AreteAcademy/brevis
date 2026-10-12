package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// warehouse is a fake connection that remembers what it was asked.
type warehouse struct {
	asked []dialect.Request
	cols  []string
	rows  [][]any
}

func (w *warehouse) Exec(context.Context, string) error          { return nil }
func (w *warehouse) Scalar(context.Context, string) (any, error) { return nil, nil }
func (w *warehouse) Target(string) string                        { return "" }
func (w *warehouse) Close(context.Context) error                 { return nil }
func (w *warehouse) Read(_ context.Context, r dialect.Request) (dialect.Result, error) {
	w.asked = append(w.asked, r)
	rows := w.rows
	if len(rows) > r.Limit {
		rows = rows[:r.Limit]
	}
	return dialect.Result{Columns: w.cols, Rows: rows}, nil
}

func serverWith(t *testing.T, w *warehouse, endpoints ...*Endpoint) http.Handler {
	t.Helper()
	cfg := &Config{
		Name:      "metrics-api",
		Env:       "local",
		Dialect:   postgres.Dialect{},
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		MaxRows:   10000,
		Endpoints: endpoints,
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func clicks(t *testing.T) *Endpoint {
	t.Helper()
	e := &Endpoint{
		Name: "clicks", Path: "/v1/clicks",
		Params: map[string]Param{"campaign": {Type: "string", MaxLen: 8}},
		SQL:    "select day, clicks from marts.clicks where w = {{ tenant }} and c = {{ campaign }}",
	}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	return e
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// THE WHOLE PATH, ONCE: a query string becomes an argument, the warehouse is
// asked the rendered SQL with it, and the rows come back as objects with
// their columns IN THE ORDER THE QUERY NAMED THEM -- a Go map would sort
// them, and `day, clicks` arriving as `clicks, day` is a different report.
func TestARequestBecomesAQueryAndItsRowsComeBack(t *testing.T) {
	w := &warehouse{cols: []string{"day", "clicks"}, rows: [][]any{{"2026-10-05", 412}, {"2026-10-06", 7}}}
	rec := get(serverWith(t, w, clicks(t)), "/v1/clicks?campaign=c-9")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := w.asked[0]; got.Query != "select day, clicks from marts.clicks where w = $1 and c = $2" ||
		len(got.Args) != 2 || got.Args[1].Value != "c-9" {
		t.Errorf("the warehouse was asked %+v", got)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, `{"data":[{"day":"2026-10-05","clicks":412},{"day":"2026-10-06","clicks":7}],"truncated":false,`) {
		t.Errorf("the body is %s", body)
	}
	var parsed struct {
		Meta struct {
			Endpoint string `json:"endpoint"`
			Rows     int    `json:"rows"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if parsed.Meta.Endpoint != "clicks" || parsed.Meta.Rows != 2 {
		t.Errorf("meta is %+v", parsed.Meta)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
}

// THE ROW CEILING CUTS AND SAYS SO. One more row than the ceiling is asked
// for, because the reader cannot otherwise tell "exactly the ceiling" from
// "more than it".
func TestTheCeilingCutsAndSaysSo(t *testing.T) {
	w := &warehouse{cols: []string{"k"}, rows: [][]any{{1}, {2}, {3}}}
	e := clicks(t)
	e.MaxRows = 2
	rec := get(serverWith(t, w, e), "/v1/clicks?campaign=x")
	if !strings.Contains(rec.Body.String(), `"truncated":true`) || strings.Contains(rec.Body.String(), `{"k":3}`) {
		t.Errorf("body %s", rec.Body)
	}
	if w.asked[0].Limit != 3 {
		t.Errorf("asked for %d rows, wanted the ceiling plus one", w.asked[0].Limit)
	}
}

func TestWhatARequestCannotMeanIsRefused(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	h := serverWith(t, w, clicks(t))
	cases := map[string]struct {
		target string
		code   int
		says   string
	}{
		"an unknown parameter":  {"/v1/clicks?campaign=x&campain=y", 400, "campain"},
		"a repeated parameter":  {"/v1/clicks?campaign=x&campaign=y", 400, "campaign"},
		"a string over max_len": {"/v1/clicks?campaign=123456789", 400, "8"},
		"a NUL":                 {"/v1/clicks?campaign=a%00b", 400, "campaign"},
		"a missing value":       {"/v1/clicks", 400, "campaign"},
		"an undeclared path":    {"/v1/nothing", 404, ""},
	}
	for name, c := range cases {
		rec := get(h, c.target)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(w.asked) != 0 {
		t.Errorf("the warehouse was asked %d time(s) by requests that were refused", len(w.asked))
	}
}

func TestOnlyGETAndHEADAreAnswered(t *testing.T) {
	h := serverWith(t, &warehouse{cols: []string{"k"}}, clicks(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/clicks?campaign=x", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST answered %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestHealthIsAnswered(t *testing.T) {
	if rec := get(serverWith(t, &warehouse{}, clicks(t)), "/health"); rec.Code != http.StatusOK {
		t.Errorf("/health answered %d", rec.Code)
	}
}
