package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// metered is a warehouse whose ESTIMATE and whose BILL can differ, which is
// the whole point of measuring one and charging the other.
type metered struct {
	fake
	estimate int64
	scanned  int64
	ran      int
	mu       sync.Mutex
}

func (m *metered) Estimate(context.Context, string) (int64, error) { return m.estimate, nil }

func (m *metered) Read(ctx context.Context, req dialect.Request) (dialect.Result, error) {
	m.mu.Lock()
	m.ran++
	m.mu.Unlock()
	res, err := m.fake.Read(ctx, req)
	res.Scanned = m.scanned
	return res, err
}

func budgeted(t *testing.T, c dialect.Conn, audit *bytes.Buffer, budget int64) *Service {
	t.Helper()
	s, err := New(Options{
		Env: EnvLocal, Rows: 100, Bytes: 10 << 30, Audit: audit,
		Budget: budget,
		Open:   func(context.Context, Table) (dialect.Conn, error) { return c, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func onConnection(connection, statement string) string {
	b, _ := json.Marshal(queryRequest{
		Target: "bigquery://" + connection + "/bronze/orders", Statement: statement, Limit: 10,
	})
	return string(b)
}

// A CONCURRENCY LIMIT IS NOT A BUDGET -- CHECKPOINT B's F6, in its own words:
// "four at a time x 10 GB each, repeated forever, is unbounded. There is no
// per-hour or per-day ceiling anywhere."
//
// Four queries of 3 GB pass the per-query ceiling of 10 GB one at a time and
// spend 12 GB between them. The budget is what sees the SUM.
func TestABudgetBoundsTheSumAndNotJustEachQuery(t *testing.T) {
	m := &metered{estimate: 3 << 30, scanned: 3 << 30}
	s := budgeted(t, m, &bytes.Buffer{}, 10<<30)

	for i := range 3 {
		if w := ask(t, s, queryBody("SELECT 1")); w.Code != http.StatusOK {
			t.Fatalf("query %d: %d %s", i, w.Code, w.Body)
		}
	}
	// 9 GB spent; the fourth would make 12 and is refused BEFORE it runs.
	w := ask(t, s, queryBody("SELECT 1"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the fourth answered %d: %s", w.Code, w.Body)
	}
	if m.ran != 3 {
		t.Errorf("%d queries reached the warehouse", m.ran)
	}
	// AND IT SAYS WHAT IS LEFT, because "no" without a number is a wall.
	for _, want := range []string{"10 GB", "9 GB"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal does not carry %q: %s", want, w.Body)
		}
	}
}

// WHAT IS SPENT IS WHAT WAS MEASURED, not what was quoted.
//
// `Result.Scanned` already says why: "a gap between the two is how somebody
// finds out that the price a refusal was built on was not the price that was
// paid". A budget that trusted the estimate would be a budget a warehouse
// could overrun silently.
func TestWhatIsSpentIsWhatTheWarehouseBilled(t *testing.T) {
	// Quoted at 1 GB, billed at 6.
	m := &metered{estimate: 1 << 30, scanned: 6 << 30}
	s := budgeted(t, m, &bytes.Buffer{}, 10<<30)

	ask(t, s, queryBody("SELECT 1"))
	// 6 GB spent. A second quote of 1 GB fits under 10 only if the first was
	// counted at its QUOTE; counted at its BILL, 6+1 still fits -- so take a
	// third, where the two answers differ: 12 against 3.
	ask(t, s, queryBody("SELECT 1"))
	w := ask(t, s, queryBody("SELECT 1"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the third answered %d, so the budget counted the quote: %s", w.Code, w.Body)
	}
}

// PER CONNECTION, because a connection is what pays the bill. Two warehouses
// are two accounts, and exhausting one must not close the other.
func TestTheBudgetIsPerConnection(t *testing.T) {
	m := &metered{estimate: 9 << 30, scanned: 9 << 30}
	s := budgeted(t, m, &bytes.Buffer{}, 10<<30)

	ask(t, s, onConnection("acme-prod", "SELECT 1"))
	if w := ask(t, s, onConnection("acme-prod", "SELECT 1")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the second on the same connection answered %d", w.Code)
	}
	if w := ask(t, s, onConnection("acme-dev", "SELECT 1")); w.Code != http.StatusOK {
		t.Errorf("a second connection was refused on the first one's spending: %d %s", w.Code, w.Body)
	}
}

// THE WINDOW ENDS AND THE BUDGET RETURNS.
func TestTheBudgetReturnsWhenTheWindowEnds(t *testing.T) {
	m := &metered{estimate: 9 << 30, scanned: 9 << 30}
	s := budgeted(t, m, &bytes.Buffer{}, 10<<30)

	ask(t, s, queryBody("SELECT 1"))
	if w := ask(t, s, queryBody("SELECT 1")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the second answered %d", w.Code)
	}
	s.spent.rewind(2 * time.Hour)
	if w := ask(t, s, queryBody("SELECT 1")); w.Code != http.StatusOK {
		t.Errorf("a new window did not return the budget: %d %s", w.Code, w.Body)
	}
}

// AN EXHAUSTED BUDGET IS AUDITED AND COUNTED, under a name of its own: "the
// service refused something" and "the money ran out" are not the same event
// to whoever is paged about it.
func TestAnExhaustedBudgetIsAuditedByName(t *testing.T) {
	var audit bytes.Buffer
	// UNDER the per-query ceiling and OVER the budget, which is the whole
	// point: a query this service would happily run, refused because the
	// hour is spent. A quote above the ceiling would be refused as
	// `too-expensive` and would never reach the budget at all.
	m := &metered{estimate: 3 << 30, scanned: 0}
	s := budgeted(t, m, &audit, 2<<30)
	ask(t, s, queryBody("SELECT 1"))

	var line record
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &line); err != nil {
		t.Fatalf("%v: %s", err, audit.String())
	}
	if line.Outcome != "over-budget" {
		t.Errorf("the line calls it %q", line.Outcome)
	}
}

// NO BUDGET IS STILL ALLOWED, and it is the default. F6 does not say a
// budget is required -- it says the ABSENCE of one must be a stated choice
// rather than an oversight. The statement is in the boot banner; this says
// the absence still works.
func TestWithoutABudgetNothingIsBounded(t *testing.T) {
	m := &metered{estimate: 9 << 30, scanned: 9 << 30}
	s := budgeted(t, m, &bytes.Buffer{}, 0)
	for i := range 5 {
		if w := ask(t, s, queryBody("SELECT 1")); w.Code != http.StatusOK {
			t.Fatalf("query %d: %d %s", i, w.Code, w.Body)
		}
	}
}

// A WAREHOUSE THAT DOES NOT PRICE CANNOT BE BUDGETED, and this test exists
// to keep that FALSE comfort from being sold as a limit.
//
// Postgres reports no bytes: `Estimate` is not implemented and `Scanned` is
// zero. So a budget bounds BigQuery and bounds nothing on Postgres -- which
// is fine, because Postgres is not metered by the byte, and dangerous only
// if somebody believes otherwise. The boot banner says which connections it
// can actually bound.
func TestAWarehouseThatDoesNotPriceIsNotBounded(t *testing.T) {
	s := budgeted(t, &fake{}, &bytes.Buffer{}, 1)
	for range 3 {
		if w := ask(t, s, queryBody("SELECT 1")); w.Code != http.StatusOK {
			t.Fatalf("%d: %s", w.Code, w.Body)
		}
	}
}
