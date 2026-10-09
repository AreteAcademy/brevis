package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// fake is a warehouse that remembers what it was asked and answers two rows.
type fake struct {
	asked []string
	limit int
}

func (f *fake) Read(_ context.Context, q string, limit int) (dialect.Result, error) {
	f.asked = append(f.asked, q)
	f.limit = limit
	return dialect.Result{
		Columns: []string{"k", "v"},
		Rows:    [][]any{{int64(1), "a"}, {int64(2), nil}},
	}, nil
}
func (f *fake) Exec(context.Context, string) error          { return nil }
func (f *fake) Scalar(context.Context, string) (any, error) { return nil, nil }
func (f *fake) Target(ref string) string                    { return ref }
func (f *fake) Close(context.Context) error                 { return nil }

func testService(t *testing.T, f *fake) *Service {
	t.Helper()
	s, err := New(Options{
		Env:  EnvLocal,
		Rows: 100,
		Open: func(context.Context, string) (dialect.Conn, error) { return f, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func post(t *testing.T, s *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/preview", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// A PREVIEW IS COMPOSED HERE AND NEVER SENT. The request carries a TARGET,
// not a statement -- an endpoint that cannot be handed SQL cannot be tricked
// into running any.
func TestAPreviewComposesItsOwnStatement(t *testing.T) {
	f := &fake{}
	w := post(t, testService(t, f), `{"target":"bigquery://acme-prod/bronze/orders","limit":10}`)

	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if len(f.asked) != 1 {
		t.Fatalf("asked %v", f.asked)
	}
	if f.asked[0] != "SELECT * FROM bronze.orders LIMIT 10" {
		t.Errorf("statement = %q", f.asked[0])
	}

	var got struct {
		Columns   []string `json:"columns"`
		Rows      [][]any  `json:"rows"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) != 2 || got.Columns[0] != "k" {
		t.Errorf("columns = %v", got.Columns)
	}
	// NULL SURVIVES THE WIRE. It arrived as nil from the warehouse and has to
	// reach the screen as nil: JSON null, not the string "null" and not "".
	if len(got.Rows) != 2 || got.Rows[1][1] != nil {
		t.Errorf("rows = %#v", got.Rows)
	}
}

// THE CEILING IS THE SERVICE'S AND THE BROWSER CANNOT RAISE IT.
//
// It CLAMPS rather than refuses. A preview is not where somebody learns a
// limit: asking for a million rows is not an error, it is a request that gets
// the most this will give.
func TestTheRowCeilingClampsAndIsNotTheCallers(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{"above the ceiling", `{"target":"bigquery://acme-prod/bronze/orders","limit":1000000}`, 100},
		{"no limit at all", `{"target":"bigquery://acme-prod/bronze/orders"}`, 100},
		{"zero", `{"target":"bigquery://acme-prod/bronze/orders","limit":0}`, 100},
		{"negative", `{"target":"bigquery://acme-prod/bronze/orders","limit":-5}`, 100},
		{"under it", `{"target":"bigquery://acme-prod/bronze/orders","limit":7}`, 7},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{}
			if w := post(t, testService(t, f), c.body); w.Code != http.StatusOK {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			if f.limit != c.want {
				t.Errorf("read with limit %d, wanted %d", f.limit, c.want)
			}
			if !strings.HasSuffix(f.asked[0], " LIMIT "+itoa(c.want)) {
				t.Errorf("statement = %q", f.asked[0])
			}
		})
	}
}

// A TARGET THAT CANNOT BE PARSED NEVER REACHES A WAREHOUSE. The refusal
// happens before anything is opened, which is what makes ParseTarget the
// guard rather than one of two.
func TestABadTargetIsRefusedBeforeConnecting(t *testing.T) {
	opened := false
	s, err := New(Options{
		Env: EnvLocal, Rows: 100,
		Open: func(context.Context, string) (dialect.Conn, error) {
			opened = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"target":"bigquery://acme-prod/bronze/orders; DROP TABLE x"}`,
		`{"target":"postgres://db/public/orders"}`,
		`{"target":""}`,
		`not json at all`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/preview", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s -> %d, wanted 400", body, w.Code)
		}
	}
	if opened {
		t.Error("it opened a connection for a request it had already refused")
	}
}

// OUTSIDE local, A SERVICE WITH NO TOKEN DOES NOT EXIST.
//
// The gateway's rule, and the reason it reads the ENVIRONMENT rather than a
// field: "a config file that could declare itself local would be a file that
// turns off authentication."
func TestItRefusesToExistWithoutAuthOutsideLocal(t *testing.T) {
	_, err := New(Options{Env: "production", Rows: 100,
		Open: func(context.Context, string) (dialect.Conn, error) { return nil, nil }})
	if err == nil {
		t.Fatal("a production service with no token was built")
	}
	if !strings.Contains(err.Error(), "BREVIS_ENV") {
		t.Errorf("the refusal does not name what decided it: %v", err)
	}

	// And with one, it exists and demands it.
	s, err := New(Options{Env: "production", Rows: 100, Token: "s3cret",
		Open: func(context.Context, string) (dialect.Conn, error) { return &fake{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"target":"bigquery://acme-prod/bronze/orders"}`

	r := httptest.NewRequest(http.MethodPost, "/v1/preview", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token -> %d, wanted 401", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/v1/preview", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer s3cret")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("with the token -> %d: %s", w.Code, w.Body)
	}
}

// GET IS NOT A PREVIEW. A target in a URL is a target in a proxy log, in a
// browser's history and in a Referer header; the body keeps it out of all
// three.
func TestOnlyPOSTIsAnswered(t *testing.T) {
	s := testService(t, &fake{})
	r := httptest.NewRequest(http.MethodGet, "/v1/preview?target=bigquery://acme-prod/bronze/orders", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET -> %d, wanted 405", w.Code)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// empty answers a result set with no rows at all.
type empty struct{ fake }

func (e *empty) Read(context.Context, string, int) (dialect.Result, error) {
	return dialect.Result{}, nil
}

// A TABLE WITH NO ROWS IS `[]` AND NEVER `null`.
//
// "No rows" is an answer, and a grid that iterates over null throws instead
// of drawing an empty table. It is the kind of defect that exists only in a
// browser -- the Go side is happy either way, which is why the assertion is
// on the BYTES and not on the decoded struct: unmarshalling turns both into
// a nil slice and would hide exactly the difference being tested.
func TestAnEmptyResultIsAnEmptyListOnTheWire(t *testing.T) {
	s, err := New(Options{Env: EnvLocal, Rows: 100,
		Open: func(context.Context, string) (dialect.Conn, error) { return &empty{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	w := post(t, s, `{"target":"bigquery://acme-prod/bronze/orders"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"rows":[]`) {
		t.Errorf("rows is not an empty list:\n%s", body)
	}
	if !strings.Contains(body, `"columns":[]`) {
		t.Errorf("columns is not an empty list:\n%s", body)
	}
	if strings.Contains(body, "null") {
		t.Errorf("something came back null:\n%s", body)
	}
}
