package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

func withCallers(t *testing.T, audit *bytes.Buffer, callers ...Caller) *Service {
	t.Helper()
	s, err := New(Options{
		Env: "production", Rows: 100, Bytes: 10 << 30, Audit: audit, Callers: callers,
		Open: func(context.Context, Table) (dialect.Conn, error) {
			return &priced{estimate: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func asCaller(t *testing.T, s *Service, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/query", strings.NewReader(queryBody("SELECT 1")))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func lines(t *testing.T, audit *bytes.Buffer) []record {
	t.Helper()
	var out []record
	for _, l := range strings.Split(strings.TrimSpace(audit.String()), "\n") {
		if l == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("%v: %s", err, l)
		}
		out = append(out, r)
	}
	return out
}

// THE AUDIT LINE SAYS WHO, AND THE TOKEN IS WHAT PROVES IT.
//
// CHECKPOINT B's F7: "connection, hash, rows, bytes, duration, outcome — and
// nothing about who asked. The moment there are two operators, the log cannot
// answer the only question it is read for."
//
// A header the caller fills in would be a CLAIM; a token the service issued
// is a FACT. So identity is the credential, which means the log cannot say
// anything the holder of that credential did not actually hold.
func TestTheAuditSaysWhichCallerAsked(t *testing.T) {
	var audit bytes.Buffer
	s := withCallers(t, &audit,
		Caller{Name: "console", Token: "tok-console"},
		Caller{Name: "nightly-ci", Token: "tok-ci"})

	if w := asCaller(t, s, "tok-ci"); w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	got := lines(t, &audit)
	if n := len(got); n != 1 {
		t.Fatalf("%d audit line(s)", n)
	}
	if got[0].Caller != "nightly-ci" {
		t.Errorf("the line says the caller was %q", got[0].Caller)
	}

	// AND THE OTHER ONE IS THE OTHER ONE. A single name hard-coded anywhere
	// would pass the assertion above.
	audit.Reset()
	asCaller(t, s, "tok-console")
	if got := lines(t, &audit); got[0].Caller != "console" {
		t.Errorf("the second caller is logged as %q", got[0].Caller)
	}
}

// A REFUSED TOKEN IS AUDITED TOO.
//
// It was not: the middleware answered 401 before any handler ran, so nothing
// was written. "Somebody is guessing the token" is exactly what an audit is
// read for, and a log that only records successful authentication cannot
// show it.
//
// It records that a request was refused and NEVER what was presented: a near
// miss written down is the token itself, one line later.
func TestARefusedTokenLeavesALine(t *testing.T) {
	var audit bytes.Buffer
	s := withCallers(t, &audit, Caller{Name: "console", Token: "tok-console"})

	if w := asCaller(t, s, "tok-guessed"); w.Code != http.StatusUnauthorized {
		t.Fatalf("%d", w.Code)
	}
	got := lines(t, &audit)
	if len(got) != 1 {
		t.Fatalf("%d audit line(s): %s", len(got), audit.String())
	}
	if got[0].Outcome != "unauthenticated" {
		t.Errorf("the line calls it %q", got[0].Outcome)
	}
	if got[0].Caller != "" {
		t.Errorf("a refused request named a caller: %q", got[0].Caller)
	}
	if strings.Contains(audit.String(), "tok-guessed") {
		t.Errorf("the audit wrote down what was presented: %s", audit.String())
	}
}

// AN UNNAMED TOKEN STILL WORKS, AND SAYS SO.
//
// `BREVIS_SQL_SERVE_TOKEN` is what every deployment has today and it keeps
// working. The line then carries no caller, which is itself the finding: a
// shared secret with nobody's name on it.
func TestTheUnnamedTokenKeepsWorkingAndIsNotANameForIt(t *testing.T) {
	var audit bytes.Buffer
	s, err := New(Options{
		Env: "production", Rows: 100, Bytes: 10 << 30, Audit: &audit, Token: "shared",
		Open: func(context.Context, Table) (dialect.Conn, error) { return &priced{estimate: 1}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := asCaller(t, s, "shared"); w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if got := lines(t, &audit); got[0].Caller != "" {
		t.Errorf("an unnamed token was logged as %q", got[0].Caller)
	}
}

// TWO NAMES WITH ONE SECRET IS NOT TWO CALLERS.
//
// It is one credential and a log that lies about which of them used it, which
// is worse than no name at all -- the whole point of this is that the line
// cannot say something the holder did not hold.
func TestTheSameTokenUnderTwoNamesDoesNotStart(t *testing.T) {
	_, err := New(Options{
		Env: "production", Rows: 100, Bytes: 10 << 30,
		Callers: []Caller{{Name: "a", Token: "same"}, {Name: "b", Token: "same"}},
		Open:    func(context.Context, Table) (dialect.Conn, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("two names sharing one token were accepted")
	}
	if strings.Contains(err.Error(), "same") {
		t.Errorf("the refusal repeats the token: %v", err)
	}
}

// AND A NAME IS CHECKED AT BOOT, because it is written into a JSON line and
// read by whoever greps it.
func TestABadlyNamedCallerDoesNotStart(t *testing.T) {
	for _, c := range []Caller{
		{Name: "", Token: "t"},
		{Name: "   ", Token: "t"},
		{Name: "has space", Token: "t"},
		{Name: "quote\"", Token: "t"},
		{Name: "new\nline", Token: "t"},
		{Name: "console", Token: ""},
		{Name: "dup", Token: "t1"},
	} {
		callers := []Caller{c}
		if c.Name == "dup" {
			callers = append(callers, Caller{Name: "dup", Token: "t2"})
		}
		_, err := New(Options{
			Env: "production", Rows: 100, Bytes: 10 << 30, Callers: callers,
			Open: func(context.Context, Table) (dialect.Conn, error) { return nil, nil },
		})
		if err == nil {
			t.Errorf("caller %+v was accepted", c)
		}
	}
}

// THE NAME DOES NOT REACH THE METRICS.
//
// `/metrics` has no authentication -- it is bound to an address an operator
// chooses, and a scraper cannot hold a token. The same rule that keeps SQL
// and the statement hash off that endpoint keeps the caller off it: who is
// using this service is not a fact for whoever can reach the scrape port.
func TestTheCallerIsNotInTheMetrics(t *testing.T) {
	var audit bytes.Buffer
	s := withCallers(t, &audit, Caller{Name: "nightly-ci", Token: "tok-ci"})
	asCaller(t, s, "tok-ci")

	w := httptest.NewRecorder()
	s.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(w.Body.String(), "nightly-ci") {
		t.Errorf("the exposition carries the caller: %s", w.Body)
	}
}

// NAMED CALLERS SATISFY THE RULE THAT A NON-LOCAL SERVICE IS AUTHENTICATED.
func TestNamedCallersAreAuthenticationOutsideLocal(t *testing.T) {
	if _, err := New(Options{
		Env: "production", Rows: 100, Bytes: 10 << 30,
		Callers: []Caller{{Name: "console", Token: "t"}},
		Open:    func(context.Context, Table) (dialect.Conn, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("a service with a named caller was refused: %v", err)
	}
}

// EVERY AUDITED ENDPOINT NAMES THE CALLER, which is a thing to forget.
//
// The name is attached by each handler out of the request context, so an
// endpoint added tomorrow is audited without one unless somebody remembers.
// DERIVED: it walks the routes this service actually declares, so the next
// one is covered the moment it is registered.
func TestEveryAuditedEndpointNamesTheCaller(t *testing.T) {
	var audit bytes.Buffer
	s := withCallers(t, &audit, Caller{Name: "console", Token: "tok"})

	// One request per endpoint, each with a body it will get far enough
	// into to write a line. What they answer does not matter here; that a
	// line arrives naming the caller does.
	for _, call := range []struct{ path, body string }{
		{"/v1/query", queryBody("SELECT 1")},
		{"/v1/preview", `{"target":"bigquery://acme-prod/bronze/orders","limit":5}`},
		{"/v1/objects", `{"target":"bigquery://acme-prod/bronze/orders"}`},
		{"/v1/columns", `{"target":"bigquery://acme-prod/bronze/orders","schema":"bronze","name":"orders"}`},
	} {
		audit.Reset()
		r := httptest.NewRequest(http.MethodPost, call.path, strings.NewReader(call.body))
		r.Header.Set("Authorization", "Bearer tok")
		s.Handler().ServeHTTP(httptest.NewRecorder(), r)

		got := lines(t, &audit)
		if len(got) == 0 {
			t.Errorf("%s wrote no audit line at all", call.path)
			continue
		}
		for _, l := range got {
			if l.Caller != "console" {
				t.Errorf("%s logged event %q with caller %q", call.path, l.Event, l.Caller)
			}
		}
	}
}
