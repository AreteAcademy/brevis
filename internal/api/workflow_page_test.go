package api_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// workflowReads answers what a workflow's page reads: the summary row (for
// its project and schedule state), its runs and its numbers.
type workflowReads struct {
	readsFake
	ind postgres.Indicators
}

func (f workflowReads) WorkflowRuns(context.Context, string, int) ([]postgres.RunSummary, error) {
	return nil, nil
}
func (f workflowReads) IndicatorsFor(context.Context, time.Duration, string) (postgres.Indicators, error) {
	return f.ind, nil
}
func (f workflowReads) RunsPerDay(context.Context, string, int) ([]postgres.Day, error) {
	return nil, nil
}
func (f workflowReads) LoadTrend(context.Context, string, int) ([]postgres.LoadDay, error) {
	return nil, nil
}

func workflowPageUI(sum postgres.WorkflowSummary, def wf.Workflow) *api.UI {
	reads := workflowReads{
		readsFake: readsFake{workflows: []postgres.WorkflowSummary{sum}},
		ind:       postgres.Indicators{Succeeded: 9, Failed: 1, Running: 1, Pending: 2, MeanDuration: 90 * time.Second},
	}
	return api.NewUI(reads, defsFake{w: def}, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

// THE TITLE IS THE SLUG IN THE MACHINE'S FACE, and above it the eyebrow says
// where the workflow lives and how it runs: its project, its schedule in
// words, and whether that schedule is on.
func TestAWorkflowPageNamesItsProjectScheduleAndState(t *testing.T) {
	ui := workflowPageUI(
		postgres.WorkflowSummary{Slug: "daily_sales", Project: "analytics", Cron: "0 4 * * *", HasSchedule: true, Active: false},
		wf.Workflow{Slug: "daily_sales", Schedule: "0 4 * * *"},
	)
	_, body := get(t, ui, "/workflows/daily_sales")
	at := strings.Index(body, "data-eyebrow")
	if at < 0 {
		t.Fatal("the page has no eyebrow")
	}
	eyebrow := body[at : at+strings.Index(body[at:], "</div>")]
	for _, want := range []string{"analytics", "every day at 04:00", "Paused"} {
		if !strings.Contains(eyebrow, want) {
			t.Errorf("the eyebrow does not say %q", want)
		}
	}
	if !strings.Contains(body, `font-mono`) || !strings.Contains(body, ">daily_sales</h1>") {
		t.Error("the title is not the slug in mono")
	}
}

// THE STATS STRIP CARRIES THE SAME FOUR NUMBERS THE CARDS DID: success and
// failure over what finished, the mean duration, and what is open.
func TestAWorkflowPageStatsAreTheSameFourNumbers(t *testing.T) {
	ui := workflowPageUI(
		postgres.WorkflowSummary{Slug: "daily_sales", Project: "analytics"},
		wf.Workflow{Slug: "daily_sales"},
	)
	_, body := get(t, ui, "/workflows/daily_sales")
	at := strings.Index(body, "<dl")
	if at < 0 {
		t.Fatal("the numbers are not a Stats strip")
	}
	stats := body[at : at+strings.Index(body[at:], "</dl>")]
	for _, want := range []string{
		"Success rate", "90.0%", "9 of 10 finished",
		"Failure rate", "10.0%", "1 run failed",
		"Mean duration", "1m 30s",
		"Open", ">3<", "1 running, 2 queued",
	} {
		if !strings.Contains(stats, want) {
			t.Errorf("the Stats strip does not say %q", want)
		}
	}
	// A manual workflow has no schedule state to state.
	if strings.Contains(body, "Paused") || strings.Contains(body, ">Active<") {
		t.Error("a workflow with no schedule is called active or paused")
	}
}
