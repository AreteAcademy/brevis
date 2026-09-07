package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// parse runs the exposition through Prometheus's own parser.
//
// The scheme is passed explicitly because a zero TextParser panics on first
// use rather than defaulting. Legacy is the one that matches what this package
// writes: a metric name is [a-zA-Z_:][a-zA-Z0-9_:]* and nothing needs quoting.
func parse(t *testing.T, text string) map[string]*dto.MetricFamily {
	t.Helper()
	p := expfmt.NewTextParser(model.LegacyValidation)
	families, err := p.TextToMetricFamilies(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Prometheus refused the exposition: %v\n%s", err, text)
	}
	return families
}

// scrape collects and renders, the way the HTTP handler does.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.WriteTo(context.Background(), &b); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	return b.String()
}

// TestTheExpositionIsExactlyTheseBytes is the reason for owning the format
// instead of importing an exporter: the output is a value, and a change to it
// is a diff somebody reads rather than a chart somebody notices.
func TestTheExpositionIsExactlyTheseBytes(t *testing.T) {
	m := New()
	m.RunFinished(context.Background(), "daily_sales", "success", "cron", 42*time.Second)

	want := `# HELP brevis_run_duration_seconds Wall time of a run, from queued to terminal.
# TYPE brevis_run_duration_seconds histogram
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="1"} 0
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="5"} 0
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="15"} 0
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="30"} 0
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="60"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="300"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="600"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="1800"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="3600"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="7200"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="21600"} 1
brevis_run_duration_seconds_bucket{status="success",workflow="daily_sales",le="+Inf"} 1
brevis_run_duration_seconds_sum{status="success",workflow="daily_sales"} 42
brevis_run_duration_seconds_count{status="success",workflow="daily_sales"} 1
# HELP brevis_run_total Runs that reached a terminal state.
# TYPE brevis_run_total counter
brevis_run_total{status="success",trigger="cron",workflow="daily_sales"} 1
`
	if got := scrape(t, m); got != want {
		t.Errorf("the exposition changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestPrometheusItselfParsesTheOutput is the check this package cannot write for
// itself. The exporter was left out to keep protobuf out of the BINARY (see
// engine-weight.sh); the reference parser still runs here, in a test, where it
// costs nothing at run time and turns "I believe the format is right" into
// something CI answers.
func TestPrometheusItselfParsesTheOutput(t *testing.T) {
	ctx := context.Background()
	m := New()
	m.RunFinished(ctx, "daily_sales", "success", "cron", 42*time.Second)
	m.RunFinished(ctx, "daily_sales", "failed", "manual", 3*time.Second)
	m.StepFinished(ctx, "daily_sales", "extract", "success", 1200*time.Millisecond)
	m.StepStarted(ctx, "daily_sales", "extract")
	m.Claimed(ctx, 250*time.Millisecond)
	m.OrphansRecovered(ctx, 2)
	if err := m.WatchSlots(func() (int, int) { return 3, 5 }); err != nil {
		t.Fatal(err)
	}

	families := parse(t, scrape(t, m))

	for _, name := range []string{
		"brevis_run_total", "brevis_run_duration_seconds",
		"brevis_step_duration_seconds", "brevis_step_attempts_total",
		"brevis_claim_latency_seconds", "brevis_orphans_recovered_total",
		"brevis_slots_in_use", "brevis_slots_limit",
	} {
		if _, ok := families[name]; !ok {
			t.Errorf("%s did not survive the round trip", name)
		}
	}

	// Parsing is not enough on its own: a histogram whose buckets are not
	// running totals parses fine and charts wrongly.
	h := families["brevis_run_duration_seconds"].GetMetric()[0].GetHistogram()
	var previous uint64
	for _, b := range h.GetBucket() {
		if b.GetCumulativeCount() < previous {
			t.Errorf("bucket le=%v went backwards: %d after %d",
				b.GetUpperBound(), b.GetCumulativeCount(), previous)
		}
		previous = b.GetCumulativeCount()
	}
}

// TestABucketCountsEverythingBelowIt is the arithmetic on its own, with values
// chosen so that per-bucket counts and running totals cannot be confused: the
// two readings differ in nine of the twelve buckets.
func TestABucketCountsEverythingBelowIt(t *testing.T) {
	ctx := context.Background()
	m := New()
	for _, d := range []time.Duration{
		20 * time.Millisecond,  // lands in (0.01, 0.05]
		300 * time.Millisecond, // lands in (0.25, 0.5]
		300 * time.Millisecond,
		7 * time.Second, // lands in (5, 10]
	} {
		m.Claimed(ctx, d)
	}

	want := []string{
		`brevis_claim_latency_seconds_bucket{le="0.01"} 0`,
		`brevis_claim_latency_seconds_bucket{le="0.05"} 1`,
		`brevis_claim_latency_seconds_bucket{le="0.1"} 1`,
		`brevis_claim_latency_seconds_bucket{le="0.25"} 1`,
		`brevis_claim_latency_seconds_bucket{le="0.5"} 3`,
		`brevis_claim_latency_seconds_bucket{le="1"} 3`,
		`brevis_claim_latency_seconds_bucket{le="2.5"} 3`,
		`brevis_claim_latency_seconds_bucket{le="5"} 3`,
		`brevis_claim_latency_seconds_bucket{le="10"} 4`,
		`brevis_claim_latency_seconds_bucket{le="30"} 4`,
		`brevis_claim_latency_seconds_bucket{le="60"} 4`,
		`brevis_claim_latency_seconds_bucket{le="300"} 4`,
		`brevis_claim_latency_seconds_bucket{le="+Inf"} 4`,
		`brevis_claim_latency_seconds_count 4`,
	}
	got := scrape(t, m)
	for _, line := range want {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("missing: %s", line)
		}
	}
	if t.Failed() {
		t.Logf("what was written:\n%s", got)
	}
}

// TestTheBucketsComeOutInNumericOrder guards the trap that sorting the lines as
// text would spring: `le` holds a number inside a string, so lexical order puts
// "+Inf" first and "5" after "3600".
func TestTheBucketsComeOutInNumericOrder(t *testing.T) {
	m := New()
	m.Claimed(context.Background(), time.Second)

	var bounds []string
	for _, line := range strings.Split(scrape(t, m), "\n") {
		if !strings.HasPrefix(line, "brevis_claim_latency_seconds_bucket") {
			continue
		}
		bounds = append(bounds, line[strings.Index(line, `le="`)+4:strings.Index(line, `"}`)])
	}
	want := []string{"0.01", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10", "30", "60", "300", "+Inf"}
	if strings.Join(bounds, ",") != strings.Join(want, ",") {
		t.Errorf("bucket order\n got: %v\nwant: %v", bounds, want)
	}
}

// TestALabelValueWithAQuoteABackslashAndANewlineSurvives is the other half of
// what an exporter would have done. An unescaped quote closes the label early
// and corrupts every line after it -- the scrape does not fail, it silently
// becomes different data.
func TestALabelValueWithAQuoteABackslashAndANewlineSurvives(t *testing.T) {
	m := New()
	m.RunFinished(context.Background(), "a\"b\\c\nd", "success", "cron", time.Second)

	text := scrape(t, m)
	if !strings.Contains(text, `workflow="a\"b\\c\nd"`) {
		t.Errorf("the value was not escaped:\n%s", text)
	}
	families := parse(t, text)
	// And it must come back out as the original string, not as the escaped one.
	for _, l := range families["brevis_run_total"].GetMetric()[0].GetLabel() {
		if l.GetName() == "workflow" && l.GetValue() != "a\"b\\c\nd" {
			t.Errorf("round trip changed the value: %q", l.GetValue())
		}
	}
}

// TestNothingConfiguredIsAValidEmptyScrape: a nil registry is what `brevis run`
// has, and the endpoint must answer with an empty body rather than a 404 or a
// panic. "Not configured" and "broken" have to look different.
func TestNothingConfiguredIsAValidEmptyScrape(t *testing.T) {
	var m *Metrics
	if got := scrape(t, m); got != "" {
		t.Errorf("a nil registry wrote %q", got)
	}
	// Every method has to tolerate it too, or the nil check is only half done.
	ctx := context.Background()
	m.RunFinished(ctx, "w", "success", "cron", time.Second)
	m.StepFinished(ctx, "w", "s", "success", time.Second)
	m.StepStarted(ctx, "w", "s")
	m.Claimed(ctx, time.Second)
	m.OrphansRecovered(ctx, 1)
	if err := m.WatchSlots(func() (int, int) { return 1, 1 }); err != nil {
		t.Errorf("WatchSlots on a nil registry: %v", err)
	}
	if err := m.WatchQueue(func(context.Context) (int, int, error) { return 1, 1, nil }); err != nil {
		t.Errorf("WatchQueue on a nil registry: %v", err)
	}
}

// TestAMetricNobodyTouchedIsAbsent: a fresh process exposes nothing, not a wall
// of zeroes. An absent series and a series reading zero mean different things,
// and only one of them is true before any run has happened.
func TestAMetricNobodyTouchedIsAbsent(t *testing.T) {
	if got := scrape(t, New()); got != "" {
		t.Errorf("a registry nobody used wrote:\n%s", got)
	}
}

// TestNoLabelCarriesARunID is the cardinality rule as an assertion. One
// unbounded label is how a metrics backend becomes the most expensive part of
// an installation, and the run id is the one every design reaches for.
func TestNoLabelCarriesARunID(t *testing.T) {
	ctx := context.Background()
	m := New()
	id := "6f1c9c1e-3a2b-4f77-9d5e-8b0a1c2d3e4f"
	m.RunFinished(ctx, "daily_sales", "success", "cron", time.Second)
	m.StepFinished(ctx, "daily_sales", "extract", "success", time.Second)
	m.StepStarted(ctx, "daily_sales", "extract")

	text := scrape(t, m)
	if strings.Contains(text, id) {
		t.Fatalf("a run id reached the exposition:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if looksLikeAUUID(line) {
			t.Errorf("this series carries something unbounded: %s", line)
		}
	}
}

// looksLikeAUUID matches 8-4-4-4-12 hex without a regexp dependency in the
// hot path of a test that runs on every commit.
func looksLikeAUUID(s string) bool {
	const shape = "8-4-4-4-12"
	_ = shape
	for i := 0; i+36 <= len(s); i++ {
		w := s[i : i+36]
		if w[8] != '-' || w[13] != '-' || w[18] != '-' || w[23] != '-' {
			continue
		}
		ok := true
		for j, c := range w {
			if j == 8 || j == 13 || j == 18 || j == 23 {
				continue
			}
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
