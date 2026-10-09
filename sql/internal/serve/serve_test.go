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

// testBytes is a byte ceiling every test can be under, since none of them
// reach a warehouse that counts.
const testBytes = 1 << 30

// fake is a warehouse that remembers what it was asked and answers two rows.
type fake struct {
	asked    []string
	limit    int
	maxBytes int64
}

func (f *fake) Read(_ context.Context, req dialect.Request) (dialect.Result, error) {
	f.asked = append(f.asked, req.Query)
	f.limit = req.Limit
	f.maxBytes = req.MaxBytes
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
		Env:   EnvLocal,
		Rows:  100,
		Bytes: testBytes,
		Open:  func(context.Context, string) (dialect.Conn, error) { return f, nil },
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
	// ELEVEN FOR TEN. It asks for one more than it will show, so that more
	// coming back is how it knows there is more -- see TestAPreviewThatCutSaysSo.
	if f.asked[0] != "SELECT * FROM bronze.orders LIMIT 11" {
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
			w := post(t, testService(t, f), c.body)
			if w.Code != http.StatusOK {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			// The warehouse is asked for one more than the ceiling; what
			// the caller is TOLD the limit was is the ceiling itself, and
			// that is the number a screen prints.
			if f.limit != c.want+1 {
				t.Errorf("read with limit %d, wanted %d", f.limit, c.want+1)
			}
			if !strings.HasSuffix(f.asked[0], " LIMIT "+itoa(c.want+1)) {
				t.Errorf("statement = %q", f.asked[0])
			}
			var got previewResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Limit != c.want {
				t.Errorf("it reported a limit of %d, wanted %d", got.Limit, c.want)
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
		Env: EnvLocal, Rows: 100, Bytes: testBytes,
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
	_, err := New(Options{Env: "production", Rows: 100, Bytes: testBytes,
		Open: func(context.Context, string) (dialect.Conn, error) { return nil, nil }})
	if err == nil {
		t.Fatal("a production service with no token was built")
	}
	if !strings.Contains(err.Error(), "BREVIS_ENV") {
		t.Errorf("the refusal does not name what decided it: %v", err)
	}

	// And with one, it exists and demands it.
	s, err := New(Options{Env: "production", Rows: 100, Bytes: testBytes, Token: "s3cret",
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

func (e *empty) Read(context.Context, dialect.Request) (dialect.Result, error) {
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
	s, err := New(Options{Env: EnvLocal, Rows: 100, Bytes: testBytes,
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

// counting answers exactly as many rows as the statement's LIMIT allows,
// which is what a warehouse does.
type counting struct {
	fake
	have int // rows the table actually holds
}

func (c *counting) Read(_ context.Context, req dialect.Request) (dialect.Result, error) {
	c.asked = append(c.asked, req.Query)
	c.limit = req.Limit
	n := c.have
	if req.Limit < n {
		n = req.Limit
	}
	rows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, []any{int64(i)})
	}
	// `Truncated` from the warehouse is FALSE here, and that is the point:
	// with a LIMIT in the statement the query matched exactly what it
	// returned, so the warehouse has nothing to report.
	return dialect.Result{Columns: []string{"k"}, Rows: rows}, nil
}

// A PREVIEW THAT CUT HAS TO SAY SO, and its own LIMIT is what hides it.
//
// Found by running the service: a five-row table under a ceiling of three
// came back with three rows and `truncated: false`. The reason is the
// composition -- `SELECT * FROM t LIMIT 3` makes the query MATCH three, so
// `totalRows` is three and the warehouse is telling the truth. Nothing was
// wrong except the question.
//
// So it asks for one more than it will show. More came back than the ceiling
// means there is more, and the extra row is dropped rather than drawn.
func TestAPreviewThatCutSaysSo(t *testing.T) {
	for _, c := range []struct {
		name      string
		have      int
		ceiling   int
		wantRows  int
		truncated bool
	}{
		{"more than the ceiling", 5, 3, 3, true},
		{"exactly the ceiling", 3, 3, 3, false},
		{"one less", 2, 3, 2, false},
		{"empty", 0, 3, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &counting{have: c.have}
			s, err := New(Options{Env: EnvLocal, Rows: c.ceiling, Bytes: testBytes,
				Open: func(context.Context, string) (dialect.Conn, error) { return f, nil }})
			if err != nil {
				t.Fatal(err)
			}
			w := post(t, s, `{"target":"bigquery://acme-prod/bronze/orders"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			var got previewResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != c.wantRows {
				t.Errorf("%d rows, wanted %d", len(got.Rows), c.wantRows)
			}
			if got.Truncated != c.truncated {
				t.Errorf("truncated = %v, wanted %v", got.Truncated, c.truncated)
			}
			// AND THE EXTRA ROW IS ASKED FOR, which is the mechanism. A
			// statement that asked for exactly the ceiling could never tell
			// a full page from a cut one.
			if !strings.HasSuffix(f.asked[0], " LIMIT "+itoa(c.ceiling+1)) {
				t.Errorf("statement = %q, wanted it to ask for one more", f.asked[0])
			}
		})
	}
}

// A PREVIEW IS NOT FREE, and this was a hole rather than a decision.
//
// `SELECT * FROM t LIMIT 20` reads the whole table on BigQuery -- a LIMIT
// does not reduce bytes scanned -- so the row ceiling bounds the screen and
// bounds the bill not at all. Twenty rows off a petabyte table is a petabyte
// somebody pays for, from a tab opened by accident.
func TestAPreviewIsBoundedByTheByteCeilingToo(t *testing.T) {
	f := &fake{}
	s := testService(t, f)

	w := post(t, s, `{"target":"bigquery://acme-prod/bronze/orders"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if f.maxBytes != testBytes {
		t.Errorf("the preview was run with a ceiling of %d bytes, and the service's is %d",
			f.maxBytes, testBytes)
	}
}

// A SERVICE WITH NO BYTE CEILING REFUSES TO EXIST, for the reason it refuses
// to exist without a token: the query that discovers the missing limit is the
// one nobody meant to run, and by then it has been billed.
func TestAServiceWithoutAByteCeilingDoesNotStart(t *testing.T) {
	_, err := New(Options{Env: EnvLocal, Rows: 100,
		Open: func(context.Context, string) (dialect.Conn, error) { return &fake{}, nil }})
	if err == nil {
		t.Fatal("a service with no byte ceiling started")
	}
	if !strings.Contains(err.Error(), "byte") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}
