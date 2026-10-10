package serve

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

const oneStatement = `{"target":"bigquery://acme-prod/bronze/orders","statement":"SELECT 1"}`

func estimate(t *testing.T, s *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/estimate", strings.NewReader(body))
	if s.opt.Token != "" {
		r.Header.Set("Authorization", "Bearer "+s.opt.Token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// A PRICE, AND NOTHING RAN.
//
// That is the whole endpoint: the one number a person needs before pressing
// Run, taken from a dry run that creates no job and bills nothing --
// measured against real BigQuery on 2026-10-09 rather than read in a
// document.
func TestEstimatePricesWithoutRunning(t *testing.T) {
	p := &priced{estimate: 2048}
	w := estimate(t, listed(t, p, &bytes.Buffer{}), oneStatement)

	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"bytes":2048`) {
		t.Errorf("the answer carries no price: %s", w.Body)
	}
	if p.ran != 0 {
		t.Errorf("a dry run ran the query %d times", p.ran)
	}
}

// IT SPENDS NO BUDGET, WHICH IS WHY IT NEEDS A LIMIT OF ITS OWN.
//
// A dry run scans nothing, so charging it against the hour's budget would
// refuse queries nobody had paid for. The consequence is that it is free to
// call in a loop, and the budget -- the thing that stops every other loop on
// this service -- cannot see it.
func TestEstimateSpendsNoBudget(t *testing.T) {
	p := &priced{estimate: 2048}
	s := listed(t, p, &bytes.Buffer{})
	s.opt.Budget = 1 << 20

	for i := 0; i < 5; i++ {
		if w := estimate(t, s, oneStatement); w.Code != http.StatusOK {
			t.Fatalf("estimate %d: %d %s", i, w.Code, w.Body)
		}
	}
	if _, used := s.spent.allows("acme-prod", 0, s.opt.Budget); used != 0 {
		t.Errorf("five dry runs spent %d bytes of the hour's budget", used)
	}
}

// AND IT IS LIMITED, BECAUSE NOTHING ELSE LIMITS IT.
//
// Every other endpoint here is bounded by what it costs: the budget stops a
// loop because a loop spends. This one spends nothing, so the bound has to
// be the calls themselves.
func TestEstimateIsRatedPerCaller(t *testing.T) {
	p := &priced{estimate: 1}
	s := listed(t, p, &bytes.Buffer{})

	for i := 0; i < estimateRate; i++ {
		if w := estimate(t, s, oneStatement); w.Code != http.StatusOK {
			t.Fatalf("call %d was refused early: %d %s", i, w.Code, w.Body)
		}
	}
	w := estimate(t, s, oneStatement)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("call %d answered %d, and nothing bounds this endpoint but this", estimateRate+1, w.Code)
	}
	// 429 AND A SENTENCE SOMEBODY CAN ACT ON. "Too many requests" with no
	// number is a wall; the number is what tells an operator whether their
	// console is misbehaving or their limit is too low.
	if !strings.Contains(w.Body.String(), "minute") {
		t.Errorf("the refusal does not say when it will answer again: %s", w.Body)
	}

	// AND THE WINDOW TURNS. A limit that never forgets is an outage.
	s.quoted.rewind(estimateWindow)
	if w := estimate(t, s, oneStatement); w.Code != http.StatusOK {
		t.Errorf("the window never turned: %d %s", w.Code, w.Body)
	}
}

// THE CEILING IS SAID HERE, WHICH IS THE WHOLE VALUE OF THE LINE.
//
// A statement this service will not run is refused the same way it would be
// refused at Run, with the same sentence, BEFORE somebody presses it.
func TestEstimateRefusesWhatTheQueryWouldRefuse(t *testing.T) {
	p := &priced{estimate: 20 << 30}
	s := listed(t, p, &bytes.Buffer{})
	s.opt.Bytes = 10 << 30

	w := estimate(t, s, oneStatement)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a statement over the ceiling answered %d: %s", w.Code, w.Body)
	}
	for _, want := range []string{"20 GB", "10 GB", "stops at"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal does not carry %q: %s", want, w.Body)
		}
	}
}

// A WAREHOUSE THAT CANNOT PRICE SAYS SO, AND IS NOT GUESSED AT.
//
// Postgres has no `Estimator`, and the obvious fallback -- `EXPLAIN` -- was
// REFUSED at CHECKPOINT E by the person who owns this decision. It returns a
// cost in planner units nobody is billed in, and a number in the wrong unit
// under a line that says "this query will process" is worse than no line:
// somebody would compare it with a BigQuery figure.
func TestAWarehouseThatCannotPriceIsNotGuessedAt(t *testing.T) {
	f := &fake{}
	w := estimate(t, listed(t, f, &bytes.Buffer{}), oneStatement)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "does not price") {
		t.Errorf("the answer does not say why there is no number: %s", w.Body)
	}
	if strings.Contains(strings.ToUpper(w.Body.String()), "EXPLAIN") {
		t.Error("the refusal offers EXPLAIN, which prices in a unit nobody is billed in")
	}
}

// THE AUDIT CARRIES THE FINGERPRINT AND NEVER THE TEXT, which is the rule
// `/v1/query` already follows -- it hashes the statement to 16 hex and logs
// that. An estimate is the same disclosure with less behind it.
func TestTheEstimateAuditCarriesNoStatementText(t *testing.T) {
	var log bytes.Buffer
	s := listed(t, &priced{estimate: 2048}, &log)
	if w := estimate(t, s, `{"target":"bigquery://acme-prod/bronze/orders","statement":"SELECT secret FROM payroll.x"}`); w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}

	line := log.String()
	if !strings.Contains(line, `"event":"estimate"`) {
		t.Errorf("no estimate was audited: %s", line)
	}
	for _, leak := range []string{"payroll", "secret", "SELECT"} {
		if strings.Contains(line, leak) {
			t.Errorf("the audit line carries %q, which is the statement: %s", leak, line)
		}
	}
	if !strings.Contains(line, `"statement":"`) {
		t.Errorf("the line carries no fingerprint, so an estimate cannot be tied to the query that followed it: %s", line)
	}
	// AND THE FIGURE IS NOT CALLED `bytes`. That field feeds the `scanned`
	// metric, and a dry run scanned nothing -- counting it there would make
	// the one number an operator reads to answer "what did we spend" include
	// bytes nobody was billed for.
	if !strings.Contains(line, `"estimated":2048`) {
		t.Errorf("the line does not record what was quoted: %s", line)
	}
	if strings.Contains(line, `"bytes":2048`) {
		t.Errorf("a dry run was recorded as bytes scanned: %s", line)
	}
}

// AND THE METRIC AGREES. `observe` adds `line.Bytes` to `scanned` per
// connection; an estimate must leave it alone.
func TestAnEstimateScansNothingInTheMetrics(t *testing.T) {
	s := listed(t, &priced{estimate: 4096}, &bytes.Buffer{})
	if w := estimate(t, s, oneStatement); w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}

	w := httptest.NewRecorder()
	s.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(w.Body.String(), "4096") {
		t.Errorf("the exposition counts a dry run as bytes scanned:\n%s", w.Body)
	}
}

// A STATEMENT IS NOT A TARGET. The endpoint takes SQL, so the classifier
// runs here as it does on /v1/query: a dry run of a DELETE is still a
// statement this service has decided it will not look at.
func TestEstimateClassifiesWhatItIsGiven(t *testing.T) {
	p := &priced{estimate: 1}
	w := estimate(t, listed(t, p, &bytes.Buffer{}),
		`{"target":"bigquery://acme-prod/bronze/orders","statement":"DELETE FROM bronze.orders"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("a DELETE was priced: %d %s", w.Code, w.Body)
	}
	if p.ran != 0 {
		t.Error("the warehouse saw a statement this service had already decided against")
	}
}

var _ dialect.Conn = (*priced)(nil)
