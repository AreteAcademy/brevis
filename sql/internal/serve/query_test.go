package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// priced is a warehouse that can say what a query would cost, and remembers
// whether it was ever asked to RUN one.
type priced struct {
	fake
	estimate int64
	fail     error
	ran      int
	mu       sync.Mutex
	block    chan struct{}
}

func (p *priced) Estimate(context.Context, string) (int64, error) {
	if p.fail != nil {
		return 0, p.fail
	}
	return p.estimate, nil
}

func (p *priced) Read(ctx context.Context, req dialect.Request) (dialect.Result, error) {
	p.mu.Lock()
	p.ran++
	p.mu.Unlock()
	if p.block != nil {
		// BOUNDED, so a missing concurrency limit FAILS rather than hangs.
		// The second query would otherwise wait for a channel the test only
		// closes after it returns, and a test that deadlocks is a test that
		// reports nothing at all -- which is the same silence the limit
		// exists to break.
		select {
		case <-p.block:
		case <-time.After(2 * time.Second):
		}
	}
	res, err := p.fake.Read(ctx, req)
	res.Scanned = p.estimate
	return res, err
}

func queryService(t *testing.T, c dialect.Conn, audit *bytes.Buffer, maxBytes int64) *Service {
	t.Helper()
	s, err := New(Options{
		Env:   EnvLocal,
		Rows:  100,
		Bytes: maxBytes,
		Audit: audit,
		Open:  func(context.Context, string) (dialect.Conn, error) { return c, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ask(t *testing.T, s *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/query", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func queryBody(statement string) string {
	b, _ := json.Marshal(queryRequest{
		Target: "bigquery://acme-prod/bronze/orders", Statement: statement, Limit: 10,
	})
	return string(b)
}

// #65's OWN CRITERION: a write is refused HERE, with the console nowhere in
// the picture. Everything the browser does is a convenience; this is the
// line, and these requests are what somebody with the token and curl sends.
func TestAWriteIsRefusedAtTheServiceItself(t *testing.T) {
	for _, stmt := range []string{
		"UPDATE bronze.orders SET total = 0",
		"DELETE FROM bronze.orders",
		"DROP TABLE bronze.orders",
		"CREATE OR REPLACE TABLE bronze.orders AS SELECT 1",
		"TRUNCATE TABLE bronze.orders",
		"SELECT 1; DROP TABLE bronze.orders",
		"WITH x AS (DELETE FROM bronze.orders RETURNING *) SELECT * FROM x",
		"GRANT SELECT ON bronze.orders TO everyone",
	} {
		p := &priced{estimate: 1}
		w := ask(t, queryService(t, p, &bytes.Buffer{}, 1<<30), queryBody(stmt))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q answered %d, and it is not a query", stmt, w.Code)
		}
		if p.ran != 0 {
			t.Errorf("%q reached the warehouse", stmt)
		}
	}
}

// A QUERY OVER THE CEILING IS REFUSED BEFORE IT RUNS, with the estimate in
// the message. "Too expensive" without a number tells somebody nothing about
// whether they mistyped a table or asked a fair question of a large one.
func TestAnExpensiveQueryIsRefusedBeforeItRuns(t *testing.T) {
	p := &priced{estimate: 4 << 40} // 4 TB
	w := ask(t, queryService(t, p, &bytes.Buffer{}, 10<<30), queryBody("SELECT * FROM bronze.orders"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d: %s", w.Code, w.Body)
	}
	if p.ran != 0 {
		t.Error("the query was refused AFTER running, which is a refusal that costs the money it saved")
	}
	for _, want := range []string{"4 TB", "10 GB"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal never says %s: %s", want, w.Body)
		}
	}
}

// A PRICE NOBODY COULD READ IS NOT PERMISSION TO RUN. The dry run failing is
// the warehouse declining to say, and running anyway spends exactly the money
// the price was asked about.
func TestAQueryThatCannotBePricedDoesNotRun(t *testing.T) {
	p := &priced{fail: fmt.Errorf("bigquery: Syntax error at [1:8]")}
	w := ask(t, queryService(t, p, &bytes.Buffer{}, 10<<30), queryBody("SELECT * FROM bronze.orders"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d: %s", w.Code, w.Body)
	}
	if p.ran != 0 {
		t.Error("a query nobody could price was run anyway")
	}
}

// AND IT RUNS, under the ceiling, saying what it cost.
func TestAQueryUnderTheCeilingRunsAndSaysWhatItScanned(t *testing.T) {
	p := &priced{estimate: 5 << 20}
	w := ask(t, queryService(t, p, &bytes.Buffer{}, 10<<30), queryBody("SELECT k, v FROM bronze.orders"))
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body)
	}
	var got queryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 2 || len(got.Columns) != 2 {
		t.Fatalf("got %d columns and %d rows", len(got.Columns), len(got.Rows))
	}
	if got.Bytes != 5<<20 {
		t.Errorf("reported %d bytes scanned, and the warehouse said %d", got.Bytes, 5<<20)
	}
	// THE BELT TRAVELS WITH IT. The price was taken a moment ago and the
	// table can grow in between.
	if p.maxBytes != 10<<30 {
		t.Errorf("the query ran with a ceiling of %d, and the service's is %d", p.maxBytes, int64(10)<<30)
	}
}

// THE STATEMENT IS THE CALLER'S, run exactly as written. A service that
// wrapped it would change what the warehouse plans and would be editing SQL
// somebody is about to be charged for.
func TestTheStatementReachesTheWarehouseAsWritten(t *testing.T) {
	const stmt = "SELECT k FROM bronze.orders WHERE day = '2026-10-09' ORDER BY k"
	p := &priced{estimate: 1}
	ask(t, queryService(t, p, &bytes.Buffer{}, 10<<30), queryBody(stmt))
	if len(p.asked) != 1 || p.asked[0] != stmt {
		t.Errorf("the warehouse was asked %q", p.asked)
	}
}

// EXACTLY ONE AUDIT LINE PER QUERY -- #65's own criterion -- INCLUDING THE
// ONES THAT WERE REFUSED. An audit that records only what succeeded is an
// audit that cannot answer the question anybody asks it.
func TestEveryQueryLeavesExactlyOneAuditLine(t *testing.T) {
	for _, c := range []struct {
		name     string
		stmt     string
		estimate int64
		outcome  string
	}{
		{"it ran", "SELECT k FROM bronze.orders", 1 << 20, "ok"},
		{"a write", "DELETE FROM bronze.orders", 1 << 20, "refused"},
		{"too expensive", "SELECT * FROM bronze.orders", 4 << 40, "too-expensive"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var audit bytes.Buffer
			p := &priced{estimate: c.estimate}
			ask(t, queryService(t, p, &audit, 10<<30), queryBody(c.stmt))

			lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
			if len(lines) != 1 || lines[0] == "" {
				t.Fatalf("%d audit lines:\n%s", len(lines), audit.String())
			}
			var line map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
				t.Fatalf("the audit line is not JSON: %s", lines[0])
			}
			if line["outcome"] != c.outcome {
				t.Errorf("the outcome is %v, wanted %q: %s", line["outcome"], c.outcome, lines[0])
			}
			if line["connection"] != "acme-prod" {
				t.Errorf("the audit line does not name the connection: %s", lines[0])
			}
			if line["statement"] == nil || line["statement"] == "" {
				t.Errorf("the audit line carries no way to tell one query from another: %s", lines[0])
			}
		})
	}
}

// NO SQL TEXT, ANYWHERE IT IS KEPT. #65's own line: a WHERE clause carries
// customer data, and an audit file outlives the question it was written for.
// A hash tells two queries apart, which is what an audit actually needs.
func TestTheAuditLineNeverHoldsTheSQL(t *testing.T) {
	const secret = "cpf_12345678900"
	var audit bytes.Buffer
	p := &priced{estimate: 1}
	s := queryService(t, p, &audit, 10<<30)

	ask(t, s, queryBody("SELECT k FROM bronze.orders WHERE cpf = '"+secret+"'"))
	ask(t, s, queryBody("DELETE FROM "+secret))

	if strings.Contains(audit.String(), secret) {
		t.Errorf("the audit line holds the statement:\n%s", audit.String())
	}
	if strings.Contains(audit.String(), "SELECT") || strings.Contains(audit.String(), "DELETE") {
		t.Errorf("the audit line holds SQL:\n%s", audit.String())
	}
}

// THE SAME QUERY IS THE SAME HASH, and a different one is not. That is the
// whole of what the hash is for: "this ran forty times today" is an answer,
// and it is the only one that survives dropping the text.
func TestTheHashTellsQueriesApartAndKeepsThemTogether(t *testing.T) {
	line := func(stmt string) map[string]any {
		var audit bytes.Buffer
		ask(t, queryService(t, &priced{estimate: 1}, &audit, 10<<30), queryBody(stmt))
		var out map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(audit.String())), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := line("SELECT k FROM bronze.orders")
	again := line("SELECT k FROM bronze.orders")
	other := line("SELECT v FROM bronze.orders")

	if a["statement"] != again["statement"] {
		t.Error("the same query hashed two ways")
	}
	if a["statement"] == other["statement"] {
		t.Error("two different queries hashed the same")
	}
}

// A ROW CEILING THE CALLER CANNOT RAISE, the preview's rule and for the
// preview's reason: a limit the caller could change is not a limit.
func TestTheRowCeilingHoldsForQueriesToo(t *testing.T) {
	p := &priced{estimate: 1}
	s := queryService(t, p, &bytes.Buffer{}, 10<<30)
	body, _ := json.Marshal(queryRequest{
		Target: "bigquery://acme-prod/bronze/orders", Statement: "SELECT 1", Limit: 1_000_000,
	})
	if w := ask(t, s, string(body)); w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	// One more than the ceiling: the probe row that detects a cut.
	if p.limit != 101 {
		t.Errorf("a caller asking for a million rows got %d", p.limit)
	}
}

// MORE AT ONCE THAN THE SERVICE WILL CARRY IS REFUSED, not queued.
//
// A queue behind a browser is a browser that waits with no way to know why,
// and every waiting request still holds a connection to the warehouse. The
// refusal is the honest answer and the one somebody can retry.
func TestTooManyAtOnceIsRefusedRatherThanQueued(t *testing.T) {
	p := &priced{estimate: 1, block: make(chan struct{})}
	s, err := New(Options{
		Env: EnvLocal, Rows: 100, Bytes: 1 << 30, Concurrent: 1, Audit: &bytes.Buffer{},
		Open: func(context.Context, string) (dialect.Conn, error) { return p, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	go func() {
		close(started)
		ask(t, s, queryBody("SELECT 1"))
	}()
	<-started
	// Wait until the first query is inside the warehouse.
	for {
		p.mu.Lock()
		in := p.ran
		p.mu.Unlock()
		if in > 0 {
			break
		}
	}

	w := ask(t, s, queryBody("SELECT 2"))
	close(p.block)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("the second query answered %d, and only one may run at a time", w.Code)
	}
}
