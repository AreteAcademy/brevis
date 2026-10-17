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
	ind  postgres.Indicators
	runs []postgres.RunSummary
}

func (f workflowReads) WorkflowRuns(context.Context, string, int) ([]postgres.RunSummary, error) {
	return f.runs, nil
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

// THE TITLE IS THE SLUG IN THE MACHINE'S FACE, and under it one meta line
// (screen-workflow-v2 WV1, where an eyebrow above it and a line below it
// were): whether its schedule is on, its project, the cron and its words,
// how many steps and dependencies, its tags.
func TestAWorkflowPageNamesItsProjectScheduleAndState(t *testing.T) {
	ui := workflowPageUI(
		postgres.WorkflowSummary{Slug: "daily_sales", Project: "analytics", Cron: "0 4 * * *", HasSchedule: true, Active: false},
		wf.Workflow{Slug: "daily_sales", Schedule: "0 4 * * *", Tags: []string{"sales"},
			Nodes: []wf.Node{{ID: "a", Run: "echo a"}, {ID: "b", Run: "echo b"}}, Edges: []wf.Edge{{From: "a", To: "b"}}},
	)
	_, body := get(t, ui, "/workflows/daily_sales")
	if strings.Contains(body, "data-eyebrow") {
		t.Error("the state is still an eyebrow above the title")
	}
	at := strings.Index(body, "data-workflow-meta")
	if at < 0 {
		t.Fatal("the page has no meta line")
	}
	meta := body[at : at+strings.Index(body[at:], "</p>")]
	for _, want := range []string{"Paused", "analytics", "0 4 * * *", "every day at 04:00", "2 steps", "1 dependency", "sales"} {
		if !strings.Contains(meta, want) {
			t.Errorf("the meta line does not say %q", want)
		}
	}
	if strings.Index(body, ">daily_sales</h1>") > at {
		t.Error("the meta line is not under the title")
	}
	if !strings.Contains(body, `font-mono`) || !strings.Contains(body, ">daily_sales</h1>") {
		t.Error("the title is not the slug in mono")
	}
}

// A SCHEDULE IS PAUSED FROM ITS WORKFLOW'S PAGE (screen-workflow-v2 WV1): one
// POST to the toggle the list uses, worded by what it will do -- Pause when it
// runs, Resume when it does not -- and absent when there is nothing to pause.
func TestAScheduleIsPausedFromItsPage(t *testing.T) {
	def := wf.Workflow{Slug: "daily_sales", Schedule: "0 4 * * *"}
	for _, c := range []struct {
		active bool
		word   string
	}{{true, "Pause"}, {false, "Resume"}} {
		_, body := get(t, workflowPageUI(postgres.WorkflowSummary{Slug: "daily_sales", Cron: "0 4 * * *", HasSchedule: true, Active: c.active}, def), "/workflows/daily_sales")
		header := body[strings.Index(body, "data-page-header"):]
		header = header[:strings.Index(header, "</header>")]
		if !strings.Contains(header, `action="/workflows/daily_sales/toggle"`) || !strings.Contains(header, c.word) {
			t.Errorf("active=%v: the header has no %s posting to the toggle", c.active, c.word)
		}
	}
	_, manual := get(t, workflowPageUI(postgres.WorkflowSummary{Slug: "daily_sales"}, wf.Workflow{Slug: "daily_sales"}), "/workflows/daily_sales")
	if strings.Contains(manual, "/toggle") {
		t.Error("a workflow with no schedule offers to pause it")
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

// A ONE-STEP WORKFLOW SHOWS ITS STEP, NOT A CANVAS (screen-workflow-v2 WV3):
// the step's line from its definition -- name, command, what it runs in --
// with a neutral mark, since no run is being shown; no graph island. Two
// steps or more keep the graph (TestTheWorkflowPageEmbedsItsGraph).
func TestAOneStepWorkflowShowsItsStep(t *testing.T) {
	ui := workflowPageUI(postgres.WorkflowSummary{Slug: "single"},
		wf.Workflow{Slug: "single", Nodes: []wf.Node{{ID: "start", Marker: true}, {ID: "load", Run: "/opt/brevis/bin/pipeline load", Runtime: "go"}},
			Edges: []wf.Edge{{From: "start", To: "load"}}})
	_, body := get(t, ui, "/workflows/single")
	if strings.Contains(body, `id="dag"`) || strings.Contains(body, "dag.js") {
		t.Error("a one-step workflow still mounts the graph island")
	}
	at := strings.Index(body, "data-step-line")
	if at < 0 {
		t.Fatal("no step line")
	}
	line := body[at:]
	line = line[:strings.Index(line, "</section>")]
	for _, want := range []string{">load<", "/opt/brevis/bin/pipeline load", ">Go<"} {
		if !strings.Contains(line, want) {
			t.Errorf("the step line lost %q", want)
		}
	}
	if strings.Contains(line, "pending") || strings.Contains(line, "data-stages") {
		t.Error("the definition's line claims a run state or stages")
	}
}

// THE WORKFLOW'S RUNS DO NOT NAME THE WORKFLOW (screen-workflow-v2 WV4): a
// panel with its count and "see all", one row per run with its state (the
// link to the run), trigger, slot, duration and when -- and no Workflow
// column repeating the page's own name. /runs keeps it.
func TestAWorkflowsRunsDropTheWorkflowColumn(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	d := 90 * time.Second
	reads := workflowReads{
		readsFake: readsFake{workflows: []postgres.WorkflowSummary{{Slug: "daily_sales", Project: "analytics", Cron: "0 4 * * *", HasSchedule: true, Active: true}}},
		runs: []postgres.RunSummary{{ID: "8d36ef6e-0000-0000-0000-000000000001", WorkflowSlug: "daily_sales", Status: "success",
			TriggerType: "schedule", CreatedAt: created, Duration: &d}},
	}
	ui := api.NewUI(reads, defsFake{w: wf.Workflow{Slug: "daily_sales", Schedule: "0 4 * * *"}}, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
	_, body := get(t, ui, "/workflows/daily_sales")

	at := strings.Index(body, "data-workflow-runs")
	if at < 0 {
		t.Fatal("the runs are not a panel")
	}
	runs := body[at:]
	if strings.Contains(runs[:strings.Index(runs, "</thead>")], ">Workflow<") {
		t.Error("the workflow's own runs still name the workflow")
	}
	for _, want := range []string{`href="/runs/8d36ef6e-0000-0000-0000-000000000001"`, "schedule", "1m 30s", `href="/runs?workflow=daily_sales"`, ">Runs<"} {
		if !strings.Contains(runs, want) {
			t.Errorf("the runs panel lost %s", want)
		}
	}

	// NOTHING THE PAGE SHOWED IS LOST: the meta, the actions, the numbers.
	for _, want := range []string{"analytics", "every day at 04:00", "Active", "Pause", "Success rate", "Failure rate", "Mean duration", "Open", "Next run", "A year of runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("the workflow page lost %q", want)
		}
	}
}
