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

// A WORKFLOW WITH PARAMETERS RUNS FROM A DRAWER: the page action "Run…" opens
// a native <dialog> holding the same fields, posting to the same trigger, with
// a preview of the payload. With no script, the action is a link to #run and
// the form shows in place (`:target`). A workflow with no parameters keeps
// the one-click ▶: there is nothing to ask.
func TestAWorkflowWithParametersRunsFromADrawer(t *testing.T) {
	ui := workflowPageUI(
		postgres.WorkflowSummary{Slug: "backfill", Project: "analytics"},
		wf.Workflow{Slug: "backfill", Params: []wf.Param{
			{Name: "since", Type: wf.ParamString, Default: "2026-01-01"},
			{Name: "dry_run", Type: wf.ParamBool, Default: "true"},
		}},
	)
	_, body := get(t, ui, "/workflows/backfill")
	if !strings.Contains(body, `href="#run"`) || !strings.Contains(body, `data-dialogo="run"`) {
		t.Error("the page has no Run… action that opens the drawer")
	}
	at := strings.Index(body, `<dialog id="run"`)
	if at < 0 {
		t.Fatal("there is no drawer")
	}
	drawer := body[at : at+strings.Index(body[at:], "</dialog>")]
	for _, want := range []string{
		`<form method="post" action="/workflows/backfill/trigger"`,
		`name="param.since"`, `value="2026-01-01"`, `name="param.dry_run"`,
		`data-run-preview`,
	} {
		if !strings.Contains(drawer, want) {
			t.Errorf("the drawer has no %s", want)
		}
	}
	if strings.Count(body, `name="param.since"`) != 1 {
		t.Error("the fields are drawn outside the drawer too")
	}
	if strings.Contains(body, `aria-label="Run backfill"`) {
		t.Error("a workflow with parameters still runs with one click, skipping them")
	}

	_, js := get(t, ui, "/assets/ui.js")
	if !strings.Contains(strip(js), "data-run-preview") {
		t.Error("ui.js does not fill the preview")
	}

	plain := workflowPageUI(postgres.WorkflowSummary{Slug: "daily"}, wf.Workflow{Slug: "daily"})
	_, body = get(t, plain, "/workflows/daily")
	if strings.Contains(body, `<dialog id="run"`) || !strings.Contains(body, `aria-label="Run daily"`) {
		t.Error("a workflow with no parameters lost its one-click run")
	}
}
