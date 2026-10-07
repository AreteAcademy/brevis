package execution

import (
	"fmt"
	"testing"
	"time"
)

// A fixed receipt clock, so `at` handling is testable.
func withClock(t *testing.T, at time.Time) {
	t.Helper()
	previous := landingClock
	landingClock = func() time.Time { return at }
	t.Cleanup(func() { landingClock = previous })
}

var receipt = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func landedLine(target string, extra string) string {
	return fmt.Sprintf(`@brevis:{"type":"landed","target":%q%s}`, target, extra)
}

func TestALandingIsConsumedAndRecorded(t *testing.T) {
	withClock(t, receipt)
	var c stageCollector
	if !c.line(landedLine("postgres://analytics/public/orders", `,"rows":2,"bytes":512`)) {
		t.Fatal("a landing reached the step's log")
	}
	got := c.Landings()
	if len(got) != 1 {
		t.Fatalf("landings = %+v", got)
	}
	l := got[0]
	if l.Target != "postgres://analytics/public/orders" || *l.Rows != 2 || *l.Bytes != 512 {
		t.Fatalf("landing = %+v", l)
	}
	if !l.At.Equal(receipt) {
		t.Fatalf("at = %v, want the receipt time when the line carries none", l.At)
	}
}

// A landing spends nothing of the phases' budget. stageCeiling protects the
// database from a step announcing phases in a loop, and a Python step that
// lands per table must not be what pushes its own phases off the screen.
func TestLandingsDoNotSpendThePhasesBudget(t *testing.T) {
	var c stageCollector
	for i := 0; i < landingCeiling; i++ {
		c.line(landedLine(fmt.Sprintf("postgres://analytics/public/t%d", i), ""))
	}
	for i := 0; i < stageCeiling; i++ {
		c.line(fmt.Sprintf(`@brevis:{"type":"stage","index":%d,"name":"map","state":"done"}`, i))
	}
	if len(c.Stages) != stageCeiling {
		t.Fatalf("stages = %d, want %d: landings ate the phases' budget", len(c.Stages), stageCeiling)
	}
	if len(c.Landings()) != landingCeiling {
		t.Fatalf("landings = %d, want %d", len(c.Landings()), landingCeiling)
	}
}

// A Python step that lands per batch reports the table's total.
func TestTheSameTargetAddsUp(t *testing.T) {
	var c stageCollector
	c.line(landedLine("bigquery://p/d/t", `,"rows":10,"at":"2026-10-07T05:00:00Z"`))
	c.line(landedLine("bigquery://p/d/t", `,"rows":5,"bytes":100,"at":"2026-10-07T05:10:00Z"`))
	c.line(landedLine("bigquery://p/d/t", `,"at":"2026-10-07T05:05:00Z"`))

	got := c.Landings()
	if len(got) != 1 {
		t.Fatalf("landings = %+v, want one", got)
	}
	l := got[0]
	if *l.Rows != 15 || *l.Bytes != 100 {
		t.Fatalf("rows=%v bytes=%v, want 15 and 100", *l.Rows, *l.Bytes)
	}
	if want := time.Date(2026, 10, 7, 5, 10, 0, 0, time.UTC); !l.At.Equal(want) {
		t.Fatalf("at = %v, want the latest, %v", l.At, want)
	}
}

// Absent is not zero: a step that does not count says nothing, and summing a
// nil into a zero would draw a table that emptied overnight.
func TestAbsentRowsStayAbsent(t *testing.T) {
	var c stageCollector
	c.line(landedLine("s3://acme/vendors/", ""))
	c.line(landedLine("s3://acme/vendors/", ""))
	if l := c.Landings()[0]; l.Rows != nil || l.Bytes != nil {
		t.Fatalf("rows=%v bytes=%v, want both absent", l.Rows, l.Bytes)
	}
}

func TestZeroRowsIsKept(t *testing.T) {
	var c stageCollector
	c.line(landedLine("s3://acme/vendors/", `,"rows":0`))
	if l := c.Landings()[0]; l.Rows == nil || *l.Rows != 0 {
		t.Fatalf("rows = %v, want an explicit 0", l.Rows)
	}
}

// A malformed landing is dropped and counted, never shown as a log line and
// never repaired.
func TestABadLandingIsRefusedAndCounted(t *testing.T) {
	var c stageCollector
	for _, bad := range []string{
		landedLine("postgres://user:pw@host:5432/db/public/t", ""),
		landedLine("bigquery://p/d/*", ""), // a pattern is a gateway's, not a step's
		landedLine("", ""),
		landedLine("postgres://analytics/public/orders", `,"rows":"12"`),
		landedLine("postgres://analytics/public/orders", `,"rows":-1`),
		landedLine("postgres://analytics/public/orders", `,"bytes":1.5`),
	} {
		if !c.line(bad) {
			t.Errorf("%s reached the step's log", bad)
		}
	}
	if n := len(c.Landings()); n != 0 {
		t.Fatalf("landings = %d, want none", n)
	}
	if c.LandingsRefused != 6 {
		t.Fatalf("refused = %d, want 6", c.LandingsRefused)
	}
}

func TestTheHundredAndFirstTargetIsDropped(t *testing.T) {
	var c stageCollector
	for i := 0; i <= landingCeiling; i++ {
		c.line(landedLine(fmt.Sprintf("postgres://analytics/public/t%d", i), `,"rows":1`))
	}
	if len(c.Landings()) != landingCeiling || c.LandingsOverCeiling != 1 {
		t.Fatalf("landings=%d over=%d", len(c.Landings()), c.LandingsOverCeiling)
	}
	// A target already counted still adds up past the ceiling: the ceiling
	// bounds how many rows a step writes to the database, not its totals.
	c.line(landedLine("postgres://analytics/public/t0", `,"rows":1`))
	if l := c.Landings()[0]; *l.Rows != 2 {
		t.Fatalf("t0 rows = %d, want 2", *l.Rows)
	}
}

func TestAnAtFromTheFutureOrUnreadableIsTheReceiptTime(t *testing.T) {
	withClock(t, receipt)
	for _, at := range []string{
		receipt.Add(6 * time.Minute).Format(time.RFC3339),
		"yesterday",
	} {
		var c stageCollector
		c.line(landedLine("mysql://shop/customers", fmt.Sprintf(`,"at":%q`, at)))
		if got := c.Landings()[0].At; !got.Equal(receipt) {
			t.Errorf("at %q became %v, want the receipt time", at, got)
		}
	}
	// A clock four minutes ahead is a skewed clock, not a lie.
	var c stageCollector
	skewed := receipt.Add(4 * time.Minute)
	c.line(landedLine("mysql://shop/customers", fmt.Sprintf(`,"at":%q`, skewed.Format(time.RFC3339))))
	if got := c.Landings()[0].At; !got.Equal(skewed) {
		t.Errorf("at = %v, want %v", got, skewed)
	}
}
