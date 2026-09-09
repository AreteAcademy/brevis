package metrics

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestAStepsMetricReachesTheExposition, through Prometheus's own parser.
//
// The parser is the whole point. A metric name the format cannot carry does not
// lose one series -- it makes Prometheus refuse the ENTIRE scrape, every other
// metric with it, and that failure is invisible from inside this package. It is
// what caught the `le="+Inf"` ordering bug.
func TestAStepsMetricReachesTheExposition(t *testing.T) {
	m := New()
	ctx := context.Background()

	m.StepMetric(ctx, "daily_sales", "load", "rows_loaded", "gauge", 48213, quiet())
	m.StepMetric(ctx, "daily_sales", "load", "vendor_rejected_total", "counter", 3, quiet())
	m.StepMetric(ctx, "daily_sales", "load", "vendor_rejected_total", "counter", 2, quiet())

	text := scrape(t, m)
	families := parse(t, text)

	gauge, ok := families["brevis_step_rows_loaded"]
	if !ok {
		t.Fatalf("the gauge is not in the exposition:\n%s", text)
	}
	if v := gauge.Metric[0].GetGauge().GetValue(); v != 48213 {
		t.Errorf("rows_loaded = %v", v)
	}
	// The labels come from the ENGINE. A step cannot supply its own, and these
	// are what make the number findable.
	labels := map[string]string{}
	for _, l := range gauge.Metric[0].Label {
		labels[l.GetName()] = l.GetValue()
	}
	if labels["workflow"] != "daily_sales" || labels["step"] != "load" {
		t.Errorf("labels = %v", labels)
	}
	// And no run id, here or anywhere: one unbounded label is how a metrics
	// backend falls over.
	if _, present := labels["run_id"]; present {
		t.Error("the run id reached a label")
	}

	counter, ok := families["brevis_step_vendor_rejected_total"]
	if !ok {
		t.Fatalf("the counter is not in the exposition:\n%s", text)
	}
	// Two reports of 3 and 2 ADD UP. A counter that took the last value would
	// be a gauge with a misleading name.
	if v := counter.Metric[0].GetCounter().GetValue(); v != 5 {
		t.Errorf("vendor_rejected_total = %v, and 3 + 2 is 5", v)
	}
}

// The gauge keeps the LAST value, which is what "how many rows did the last run
// load" means.
func TestAGaugeKeepsTheLastValue(t *testing.T) {
	m := New()
	ctx := context.Background()
	for _, v := range []float64{10, 20, 7} {
		m.StepMetric(ctx, "w", "s", "watermark_age_seconds", "gauge", v, quiet())
	}
	f := parse(t, scrape(t, m))["brevis_step_watermark_age_seconds"]
	if f == nil {
		t.Fatal("the gauge is missing")
	}
	if v := f.Metric[0].GetGauge().GetValue(); v != 7 {
		t.Errorf("value = %v, wanted the last one", v)
	}
}

// TestAHostileNameCannotBreakTheScrape.
//
// This is the failure worth preventing: not a missing metric, but a scrape
// Prometheus refuses in full. The names below are what somebody actually types.
func TestAHostileNameCannotBreakTheScrape(t *testing.T) {
	m := New()
	ctx := context.Background()
	for _, bad := range []string{
		"rows-loaded", "rows loaded", "2rows", "rows\nloaded",
		`rows"loaded`, "rows{a=1}", "", "métrica",
	} {
		m.StepMetric(ctx, "w", "s", bad, "gauge", 1, quiet())
	}
	// One good one, so the assertion is not passing on an empty exposition.
	m.StepMetric(ctx, "w", "s", "good_one", "gauge", 1, quiet())

	text := scrape(t, m)
	families := parse(t, text) // fails the test if Prometheus refuses it
	if _, ok := families["brevis_step_good_one"]; !ok {
		t.Errorf("the valid metric is missing:\n%s", text)
	}
	for name := range families {
		if strings.ContainsAny(name, "- {\"\n") {
			t.Errorf("a hostile name reached the exposition: %q", name)
		}
	}
}

// TestTheNameCeilingHolds.
//
// `metrics.set(f"rows_{customer}", n)` in a loop is what this exists for: an
// uncapped registry grows until the SCHEDULER dies, taking the runs with it.
func TestTheNameCeilingHolds(t *testing.T) {
	m := New()
	ctx := context.Background()
	for i := 0; i < customCeiling*3; i++ {
		m.StepMetric(ctx, "w", "s", fmt.Sprintf("rows_customer_%d", i), "gauge", 1, quiet())
	}

	families := parse(t, scrape(t, m))
	var custom int
	for name := range families {
		if strings.HasPrefix(name, prefix) {
			custom++
		}
	}
	if custom > customCeiling {
		t.Errorf("%d custom series registered, and the ceiling is %d", custom, customCeiling)
	}
	if custom < customCeiling/2 {
		t.Errorf("only %d registered; the ceiling is not a ceiling, it is a wall", custom)
	}

	// And a name already registered still works after the ceiling is hit: the
	// cap is on NAMES, so a step reporting one number per second is free.
	m.StepMetric(ctx, "w", "s", "rows_customer_0", "gauge", 99, quiet())
	f := parse(t, scrape(t, m))["brevis_step_rows_customer_0"]
	if f == nil || f.Metric[0].GetGauge().GetValue() != 99 {
		t.Error("a registered name stopped working once the ceiling was full")
	}
}

// A step's metric cannot collide with the engine's own. Without the prefix a
// step could declare `brevis_run_total` and have its numbers added to the
// engine's -- a dashboard that lies, rather than one that is missing something.
func TestAStepCannotOverwriteTheEnginesOwnMetrics(t *testing.T) {
	m := New()
	ctx := context.Background()
	m.RunFinished(ctx, "w", "success", "schedule", 0)
	m.StepMetric(ctx, "w", "s", "brevis_run_total", "counter", 9999, quiet())

	families := parse(t, scrape(t, m))
	own := families["brevis_run_total"]
	if own == nil {
		t.Fatal("the engine's own metric vanished")
	}
	if v := own.Metric[0].GetCounter().GetValue(); v != 1 {
		t.Errorf("brevis_run_total = %v; a step reached the engine's own series", v)
	}
	if _, ok := families["brevis_step_brevis_run_total"]; !ok {
		t.Error("the step's metric went nowhere at all")
	}
}

// An unknown kind is IGNORED rather than guessed at. A newer SDK reporting a
// histogram would otherwise land on a dashboard as a gauge, meaning something
// else entirely.
func TestAnUnknownKindIsIgnored(t *testing.T) {
	m := New()
	m.StepMetric(context.Background(), "w", "s", "x", "histogram", 1, quiet())
	if _, ok := parse(t, scrape(t, m))["brevis_step_x"]; ok {
		t.Error("an unknown kind was recorded as something")
	}
}
