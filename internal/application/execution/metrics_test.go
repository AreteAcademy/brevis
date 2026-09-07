package execution_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/local"
	"github.com/AreteAcademy/brevis/internal/observability/metrics"
)

// scrapeOf runs a collect, the way the endpoint does.
func scrapeOf(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.WriteTo(context.Background(), &b); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	return b.String()
}

func mustHave(t *testing.T, text string, lines ...string) {
	t.Helper()
	for _, want := range lines {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("missing: %s", want)
		}
	}
	if t.Failed() {
		t.Logf("what the scrape said:\n%s", text)
	}
}

// TestEveryStepThatRanShowsUpOnTheScrape is the concrete form of "a number that
// is always zero is worse than no number": it drives the real DAG through the
// real runner and reads the numbers back out of the exposition, rather than
// asserting that an instrument was created.
func TestEveryStepThatRanShowsUpOnTheScrape(t *testing.T) {
	reg := execution.NewRegistry()
	for _, name := range []string{"extract", "transform"} {
		reg.MustRegister(execution.FuncTask{TaskName: name,
			Fn: func(context.Context, execution.Input) error { return nil }})
	}

	m := metrics.New()
	w := wf.Workflow{
		Slug:  "daily_sales",
		Nodes: []wf.Node{{ID: "extract", Action: "extract"}, {ID: "transform", Action: "transform"}},
		Edges: []wf.Edge{{From: "extract", To: "transform"}},
	}
	if err := (app.Runner{Go: local.NewGoExecutor(reg), Metrics: m}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	mustHave(t, scrapeOf(t, m),
		`brevis_step_attempts_total{step="extract",workflow="daily_sales"} 1`,
		`brevis_step_attempts_total{step="transform",workflow="daily_sales"} 1`,
		`brevis_step_duration_seconds_count{status="success",step="extract",workflow="daily_sales"} 1`,
		`brevis_step_duration_seconds_count{status="success",step="transform",workflow="daily_sales"} 1`,
	)
}

// TestAFlappingStepIsCountedOnceForEachAttempt is the number this feature
// exists for. A step that fails twice and passes on the third try leaves a run
// marked SUCCESS and a screen showing green; the attempt counter is the only
// place the three attempts are visible.
func TestAFlappingStepIsCountedOnceForEachAttempt(t *testing.T) {
	reg := execution.NewRegistry()
	var attempts atomic.Int32
	reg.MustRegister(execution.FuncTask{TaskName: "flaky", Fn: func(context.Context, execution.Input) error {
		if attempts.Add(1) < 3 {
			return fmt.Errorf("not this time")
		}
		return nil
	}})

	m := metrics.New()
	w := wf.Workflow{Slug: "nightly", Nodes: []wf.Node{{ID: "load", Action: "flaky"}}}
	err := app.Runner{
		Go: local.NewGoExecutor(reg), Metrics: m,
		MaxAttempts: 3, BackoffBase: time.Millisecond,
	}.Run(context.Background(), w)
	if err != nil {
		t.Fatalf("it should have succeeded on the third attempt: %v", err)
	}

	mustHave(t, scrapeOf(t, m),
		`brevis_step_attempts_total{step="load",workflow="nightly"} 3`,
		// Two failed attempts and one success, kept apart. Folding them into
		// one "the step succeeded" is what makes flapping invisible.
		`brevis_step_duration_seconds_count{status="failed",step="load",workflow="nightly"} 2`,
		`brevis_step_duration_seconds_count{status="success",step="load",workflow="nightly"} 1`,
	)
}

// TestADurationIsThePlausibleOne asserts a RANGE, not merely a non-zero.
//
// The first version of this test checked for "0" and it did not bite: replacing
// `started := time.Now()` with a zero time.Time makes time.Since return the
// interval since year one -- about 64 billion seconds -- which is not zero and
// sails through. A wrong clock and an absent clock fail in opposite directions,
// so the assertion has to have two sides.
func TestADurationIsThePlausibleOne(t *testing.T) {
	const slept = 5 * time.Millisecond

	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{TaskName: "slow", Fn: func(context.Context, execution.Input) error {
		time.Sleep(slept)
		return nil
	}})

	m := metrics.New()
	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "s", Action: "slow"}}}
	if err := (app.Runner{Go: local.NewGoExecutor(reg), Metrics: m}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	text := scrapeOf(t, m)
	const prefix = `brevis_step_duration_seconds_sum{status="success",step="s",workflow="w"} `
	i := strings.Index(text, prefix)
	if i < 0 {
		t.Fatalf("no sum at all:\n%s", text)
	}
	rest := text[i+len(prefix):]
	seconds, err := strconv.ParseFloat(rest[:strings.Index(rest, "\n")], 64)
	if err != nil {
		t.Fatalf("the sum is not a number: %v", err)
	}

	// The ceiling is generous on purpose -- a loaded CI machine is slow, and a
	// flaky test is worse than a loose one. It is still eight orders of
	// magnitude below what an unset start time produces.
	if seconds < slept.Seconds() || seconds > 60 {
		t.Errorf("a step that slept %s recorded %g seconds", slept, seconds)
	}
}

// TestARunnerWithNoRegistryStillRuns: `brevis run` on a laptop has no endpoint
// to scrape and passes no registry. If the nil path ever panics, the local CLI
// breaks for a feature it does not use.
func TestARunnerWithNoRegistryStillRuns(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{TaskName: "t",
		Fn: func(context.Context, execution.Input) error { return nil }})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "t"}}}
	if err := (app.Runner{Go: local.NewGoExecutor(reg)}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}
