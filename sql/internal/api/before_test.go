package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// daily is an endpoint with a date and a string, and the before under test.
func daily(t *testing.T, before Before) *Endpoint {
	t.Helper()
	e := &Endpoint{
		Name: "daily", Path: "/v1/daily",
		Params: map[string]Param{
			"day":      {Type: "date"},
			"campaign": {Type: "string", MaxLen: 16},
		},
		SQL: "select k from t where w = {{ tenant }}" +
			"{% if day %} and d = {{ day }}{% endif %}" +
			"{% if campaign %} and c = {{ campaign }}{% endif %}",
	}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	e.before = before
	return e
}

// beforeServer serves one endpoint, open, writing its audit lines to audit.
func beforeServer(t *testing.T, w *warehouse, e *Endpoint, audit *bytes.Buffer) *Server {
	t.Helper()
	cfg := &Config{
		Name: "metrics-api", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 10,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Endpoints: []*Endpoint{e},
	}
	// A nil *bytes.Buffer in an io.Writer is not a nil writer.
	if audit != nil {
		cfg.Audit = audit
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func lastLine(t *testing.T, audit *bytes.Buffer) record {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	var r record
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		t.Fatalf("the audit line is not JSON: %v", err)
	}
	return r
}

// dayFirst reads `14/10/2026` as `2026-10-14`, which the date type refuses.
func dayFirst(_ context.Context, r *Request) error {
	if d := r.Query.Get("day"); strings.Count(d, "/") == 2 {
		p := strings.Split(d, "/")
		r.Query.Set("day", p[2]+"-"+p[1]+"-"+p[0])
	}
	return nil
}

// THE HOOK TRANSLATES, THE API STILL VALIDATES: a format the date type
// refuses arrives as one it takes, and is bound as a date.
func TestABeforeRewritesWhatTheCallerSent(t *testing.T) {
	w := &warehouse{cols: []string{"k"}, rows: [][]any{{1}}}
	rec := get(beforeServer(t, w, daily(t, dayFirst), nil), "/v1/daily?day=14/10/2026")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	args := w.asked[0].Args
	if len(args) != 2 || args[1].Value != (dialect.Date{Year: 2026, Month: time.October, Day: 14}) {
		t.Errorf("the warehouse was asked with %+v", args)
	}
}

func TestWithoutABeforeTheSameFormatIsRefused(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	if rec := get(beforeServer(t, w, daily(t, nil), nil), "/v1/daily?day=14/10/2026"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// REFUSE IS THE ONE WAY A HOOK SPEAKS TO THE CALLER, and it is heard as
// written.
func TestARefusalIsA400WithItsMessage(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	var audit bytes.Buffer
	refuseAll := func(context.Context, *Request) error { return Refuse("the window is wider than 90 days") }
	rec := get(beforeServer(t, w, daily(t, refuseAll), &audit), "/v1/daily")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "the window is wider than 90 days" {
		t.Errorf("the body is %s", rec.Body)
	}
	if len(w.asked) != 0 {
		t.Error("a refused request reached the warehouse")
	}
	if line := lastLine(t, &audit); line.Hook != "before" {
		t.Errorf("the audit line does not say the before decided: %+v", line)
	}
}

// ANY OTHER ERROR IS THE AUTHOR'S, NOT THE CALLER'S: a 500 that says where
// the reason is, and the reason in the audit line -- never in the body.
func TestAnErrorIsA500WithTheReasonOnlyInTheAudit(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	var audit bytes.Buffer
	failing := func(context.Context, *Request) error { return errors.New("lookup table unreachable at 10.0.0.7") }
	rec := get(beforeServer(t, w, daily(t, failing), &audit), "/v1/daily")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Errorf("the hook's error reached the caller: %s", rec.Body)
	}
	line := lastLine(t, &audit)
	if line.Hook != "before" || !strings.Contains(line.Reason, "10.0.0.7") {
		t.Errorf("the audit line is %+v", line)
	}
}

// A PANIC IS ONE REQUEST'S FAILURE, not the process's.
func TestAPanicIsA500AndTheNextRequestIsServed(t *testing.T) {
	w := &warehouse{cols: []string{"k"}, rows: [][]any{{1}}}
	var audit bytes.Buffer
	panicky := func(_ context.Context, r *Request) error {
		if r.Query.Get("campaign") == "boom" {
			panic("an ordinary bug in a hook")
		}
		return nil
	}
	s := beforeServer(t, w, daily(t, panicky), &audit)
	if rec := get(s, "/v1/daily?campaign=boom"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("a panicking hook answered %d", rec.Code)
	}
	if line := lastLine(t, &audit); line.Hook != "before" || !strings.Contains(line.Reason, "panic") {
		t.Errorf("the audit line is %+v", line)
	}
	if rec := get(s, "/v1/daily?campaign=calm"); rec.Code != http.StatusOK {
		t.Fatalf("the request after the panic answered %d", rec.Code)
	}
}

// THE TENANT IS THE KEY'S, whoever writes the query: a hook that adds one is
// refused exactly as a caller who sends one.
func TestABeforeCannotSetTheTenant(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	sneaky := func(_ context.Context, r *Request) error { r.Query.Set("tenant", "globex"); return nil }
	if rec := get(beforeServer(t, w, daily(t, sneaky), nil), "/v1/daily"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(w.asked) != 0 {
		t.Error("the warehouse was asked")
	}
}

func TestABeforeCannotAddAnUndeclaredParameter(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	adds := func(_ context.Context, r *Request) error { r.Query.Set("limit", "1000000"); return nil }
	if rec := get(beforeServer(t, w, daily(t, adds), nil), "/v1/daily"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// THE AUDIT LINE IS WHAT THE CALLER SENT, not what the hook made of it:
// it answers "who asked for what", and the hook is not who asked.
func TestTheAuditKeepsWhatTheCallerSent(t *testing.T) {
	w := &warehouse{cols: []string{"k"}, rows: [][]any{{1}}}
	var audit bytes.Buffer
	adds := func(_ context.Context, r *Request) error { r.Query.Set("campaign", "c-9"); return nil }
	if rec := get(beforeServer(t, w, daily(t, adds), &audit), "/v1/daily?day=2026-10-14"); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if line := lastLine(t, &audit); len(line.Params) != 1 || line.Params[0] != "day" || line.Hook != "" {
		t.Errorf("the audit line is %+v", line)
	}
}

func TestABeforeSeesTheEndpointAndTheTenant(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	var seen Request
	look := func(_ context.Context, r *Request) error { seen = *r; return nil }
	get(beforeServer(t, w, daily(t, look), nil), "/v1/daily")
	if seen.Endpoint != "daily" || seen.Tenant != LocalTenant {
		t.Errorf("the hook saw %+v", seen)
	}
}

// THE KEY NEVER REACHES THE HOOK, and a request without one never reaches
// the hook at all.
func TestABeforeNeverSeesTheKeyAndRunsOnlyBehindIt(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	calls, auth, other := 0, "unset", ""
	look := func(_ context.Context, r *Request) error {
		calls++
		auth, other = r.Header.Get("Authorization"), r.Header.Get("X-Request-Id")
		return nil
	}
	key := "key-for-acme-0123456789abcdef0123"
	t.Setenv("TEST_API_KEYS", hashOf(key)+"=acme\n")
	s, err := New(&Config{
		Name: "metrics-api", Env: "production",
		Listen:  Listen{Auth: &Auth{Type: "bearer", KeysFrom: "TEST_API_KEYS"}},
		Dialect: postgres.Dialect{}, MaxRows: 10,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Endpoints: []*Endpoint{daily(t, look)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := withKey(s, "/v1/daily", "not-a-key-0123456789abcdef012345"); rec.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("an unknown key answered %d after %d hook call(s)", rec.Code, calls)
	}
	rec := httptestGet(s, "/v1/daily", map[string]string{"Authorization": "Bearer " + key, "X-Request-Id": "r-1"})
	if rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("a good key answered %d after %d hook call(s)", rec.Code, calls)
	}
	if auth != "" || other != "r-1" {
		t.Errorf("the hook saw Authorization %q and X-Request-Id %q", auth, other)
	}
}

func httptestGet(h http.Handler, target string, header map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}
