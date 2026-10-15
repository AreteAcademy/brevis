package api_test

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// runsReads answers the Runs list: the page's runs and the counts per state.
// asked keeps the filter the counts were read with.
type runsReads struct {
	readsFake
	runs   []postgres.RunSummary
	counts map[string]int
	asked  *postgres.RunFilter
}

func (f runsReads) Runs(context.Context, postgres.RunFilter) ([]postgres.RunSummary, error) {
	return f.runs, nil
}
func (f runsReads) CountRuns(context.Context, postgres.RunFilter) (int, error) {
	return len(f.runs), nil
}
func (f runsReads) CountRunsByState(_ context.Context, q postgres.RunFilter) (map[string]int, error) {
	if f.asked != nil {
		*f.asked = q
	}
	return f.counts, nil
}

func dur(d time.Duration) *time.Duration { return &d }

func runsUI(reads runsReads) *api.UI {
	return api.NewUI(reads, nil, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

// THE STATE FILTER IS SEGMENTED, EACH CHOICE WITH ITS COUNT -- read with the
// rest of the filter (the workflow here) and without the state, or every
// other choice would read 0.
func TestTheRunsStatesCountWhatEachWouldShow(t *testing.T) {
	var asked postgres.RunFilter
	ui := runsUI(runsReads{
		counts: map[string]int{"success": 7, "failed": 2, "running": 1},
		asked:  &asked,
	})
	_, body := get(t, ui, "/runs?workflow=daily_sales&state=failed")
	if asked.State != "" || asked.Workflow != "daily_sales" {
		t.Errorf("the counts were read with %+v", asked)
	}
	if !strings.Contains(body, `aria-label="State"`) {
		t.Fatal("the state filter is not Segmented")
	}
	for _, s := range []struct{ label, count string }{
		{"Any state", "10"}, {"Success", "7"}, {"Failure", "2"}, {"Running", "1"}, {"Queued", "0"},
	} {
		if !segment(s.label, s.count).MatchString(body) {
			t.Errorf("the filter does not say %s %s", s.label, s.count)
		}
	}
	if regexp.MustCompile(`>\s*Retrying\s*<`).MatchString(body) {
		t.Error("a state with no run is offered beyond the usual four")
	}
	if !strings.Contains(body, `href="/runs?state=success&amp;workflow=daily_sales"`) {
		t.Error("a state choice drops the workflow")
	}
}

// THE DURATION GETS A BAR, AGAINST THE PAGE'S LONGEST RUN: the eye finds the
// slow one before reading a number. A run with no duration has no bar.
// A RETRY SAYS SO: `attempt` counts from 0, so the second try is "attempt 2"
// and a first try says nothing.
func TestTheRunsRowsBarTheirDurationAndNameARetry(t *testing.T) {
	ui := runsUI(runsReads{runs: []postgres.RunSummary{
		{ID: "a", WorkflowSlug: "slow", Status: "success", Duration: dur(4 * time.Minute)},
		{ID: "b", WorkflowSlug: "half", Status: "failed", Duration: dur(2 * time.Minute), Attempt: 1},
		{ID: "c", WorkflowSlug: "queued", Status: "queued"},
	}})
	_, body := get(t, ui, "/runs")
	for _, want := range []string{`data-duration-bar style="width:100%;"`, `data-duration-bar style="width:50%;"`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s", want)
		}
	}
	if strings.Count(body, "data-duration-bar") != 2 {
		t.Error("a run with no duration has a bar")
	}
	if strings.Count(body, "attempt 2") != 1 || strings.Contains(body, "attempt 1") {
		t.Error("the retry is not named once as attempt 2")
	}
}
