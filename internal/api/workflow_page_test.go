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
	days []postgres.Day
	// day is the day the history was asked for, when one was.
	day **time.Time
}

func (f workflowReads) WorkflowHistory(_ context.Context, _ string, day *time.Time, _ int) ([]postgres.RunSummary, error) {
	if f.day != nil {
		*f.day = day
	}
	return f.runs, nil
}
func (f workflowReads) IndicatorsFor(context.Context, time.Duration, string) (postgres.Indicators, error) {
	return f.ind, nil
}
func (f workflowReads) RunsPerDay(context.Context, string, int) ([]postgres.Day, error) {
	return f.days, nil
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
			Nodes: []wf.Node{{ID: "a", Run: "python a.py"}, {ID: "b", Run: "dbt build"}}, Edges: []wf.Edge{{From: "a", To: "b"}}},
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
	for _, want := range []string{"Paused", "analytics", "0 4 * * *", "every day at 04:00", "2 steps", "1 dependency", "sales",
		`aria-label="Python"`, ">Python<", `aria-label="dbt"`} {
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

// FIVE NUMBERS, OVER THIRTY DAYS (screen-workflows T5): how many of the days
// it ran had no failure, how many had one, how long a run takes, what runs
// now, and when it fires next. Days, not runs: a */5 that failed once in a
// day of 288 runs had a bad day, and a rate over runs would call it 99.7%.
func TestAWorkflowPageHasFiveNumbers(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	reads := workflowReads{
		readsFake: readsFake{workflows: []postgres.WorkflowSummary{{Slug: "daily_sales", Project: "analytics"}}},
		ind:       postgres.Indicators{Succeeded: 9, Failed: 1, Running: 1, Pending: 2, MeanDuration: 90 * time.Second},
		days: []postgres.Day{
			{Date: today.AddDate(0, 0, -40), Total: 1, Failed: 1}, // older than the window
			{Date: today.AddDate(0, 0, -3), Total: 4, Succeeded: 4},
			{Date: today.AddDate(0, 0, -2), Total: 4, Succeeded: 3, Failed: 1},
			{Date: today.AddDate(0, 0, -1), Total: 4, Succeeded: 4},
		},
	}
	ui := api.NewUI(reads, defsFake{w: wf.Workflow{Slug: "daily_sales"}}, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
	_, body := get(t, ui, "/workflows/daily_sales")
	at := strings.Index(body, "<dl")
	if at < 0 {
		t.Fatal("the numbers are not a Stats strip")
	}
	stats := body[at : at+strings.Index(body[at:], "</dl>")]
	for _, want := range []string{
		"Days without failure", "66.7%", "2 of 3 days",
		"Days with failure", ">1<", "last 30 days",
		"Mean duration", "1m 30s",
		"In progress", ">3<", "1 running, 2 queued",
		"Next run", "manual only",
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
	// No parameters: Run… posts the trigger with no script, and with one opens
	// the dialog that asks when.
	if strings.Contains(body, `<dialog id="run"`) || !strings.Contains(body, `action="/workflows/daily/trigger"`) ||
		!strings.Contains(body, `data-dialogo="run-now-daily"`) || !strings.Contains(body, `<dialog id="run-now-daily"`) {
		t.Error("a workflow with no parameters does not run from its Run dialog")
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

// THE RUNS TAB IS THE LAST TEN, OR A DAY'S (screen-workflows T5): state,
// trigger -- a manual run of a past slot says so -- slot, the parameters it
// was given that differ from the defaults, duration and when. No Workflow
// column repeating the page's own name; /runs keeps it.
func TestAWorkflowsRunsTabSaysWhatEachRunWasGiven(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	slot := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	d := 90 * time.Second
	var asked *time.Time
	reads := workflowReads{
		readsFake: readsFake{workflows: []postgres.WorkflowSummary{{Slug: "daily_sales", Project: "analytics", Cron: "0 4 * * *", HasSchedule: true, Active: true}}},
		runs: []postgres.RunSummary{
			{ID: "8d36ef6e-0000-0000-0000-000000000001", WorkflowSlug: "daily_sales", Status: "success",
				TriggerType: "schedule", CreatedAt: created, Duration: &d, Params: map[string]string{"full": "false"}},
			{ID: "8d36ef6e-0000-0000-0000-000000000002", WorkflowSlug: "daily_sales", Status: "failed",
				TriggerType: "manual", LogicalDate: &slot, CreatedAt: created, Params: map[string]string{"full": "true", "region": "br"}},
		},
		day: &asked,
	}
	def := wf.Workflow{Slug: "daily_sales", Schedule: "0 4 * * *", Params: []wf.Param{
		{Name: "full", Type: wf.ParamBool, Default: "false"}, {Name: "region", Type: wf.ParamString, Default: "br"},
	}}
	ui := api.NewUI(reads, defsFake{w: def}, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
	_, body := get(t, ui, "/workflows/daily_sales")

	at := strings.Index(body, `<section id="runs"`)
	if at < 0 {
		t.Fatal("there is no Runs tab")
	}
	runs := body[at : at+strings.Index(body[at:], "</section>")]
	if strings.Contains(runs[:strings.Index(runs, "</thead>")], ">Workflow<") {
		t.Error("the workflow's own runs still name the workflow")
	}
	for _, want := range []string{
		`href="/runs/8d36ef6e-0000-0000-0000-000000000001"`, "schedule", "1m 30s", ">default<",
		"manual · slot", "full=true", `href="/runs?workflow=daily_sales"`,
	} {
		if !strings.Contains(runs, want) {
			t.Errorf("the Runs tab lost %s", want)
		}
	}
	if strings.Contains(runs, "region=br") {
		t.Error("a value equal to its default is listed as given")
	}
	if asked != nil {
		t.Errorf("the page asked for a day with none chosen: %v", asked)
	}

	_, body = get(t, ui, "/workflows/daily_sales?day=2026-10-26")
	if asked == nil || asked.Format("2006-01-02") != "2026-10-26" {
		t.Fatalf("the day asked = %v", asked)
	}
	if !strings.Contains(body, "data-day-filter") || !strings.Contains(body, `href="/workflows/daily_sales#runs"`) {
		t.Error("a day's runs do not say the day, with a way out")
	}

	// NOTHING THE PAGE SHOWED IS LOST: the meta, the actions, the numbers.
	for _, want := range []string{"analytics", "every day at 04:00", "Active", "Pause", "Days without failure", "Next run", "A year of runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("the workflow page lost %q", want)
		}
	}
}

// THE PARAMETERS TAB IS WHAT THE WORKFLOW ACCEPTS: name, type, default and
// what it is for -- its title, then its description -- with its group and the
// connector it bills. A workflow with none says what every step still gets.
func TestAWorkflowsParametersTabIsWhatItAccepts(t *testing.T) {
	def := wf.Workflow{Slug: "daily_sales", Params: []wf.Param{
		{Name: "max_queries", Type: wf.ParamInteger, Default: "300", Title: "Queries", Description: "How many queries it may run.", Group: "Cost", Uses: "bigquery"},
	}}
	_, body := get(t, workflowPageUI(postgres.WorkflowSummary{Slug: "daily_sales"}, def), "/workflows/daily_sales")
	at := strings.Index(body, `<section id="params"`)
	if at < 0 {
		t.Fatal("there is no Parameters tab")
	}
	tab := body[at : at+strings.Index(body[at:], "</section>")]
	for _, want := range []string{"max_queries", "integer", "300", "Queries", "How many queries it may run.", "Cost", `aria-label="Google BigQuery"`} {
		if !strings.Contains(tab, want) {
			t.Errorf("the Parameters tab has no %s", want)
		}
	}

	_, body = get(t, workflowPageUI(postgres.WorkflowSummary{Slug: "plain"}, wf.Workflow{Slug: "plain"}), "/workflows/plain")
	at = strings.Index(body, `<section id="params"`)
	if at < 0 || !strings.Contains(body[at:], "$BREVIS_AUTO_*") {
		t.Error("a workflow with no parameters does not say what every step still gets")
	}
}

// THE DEFINITION TAB IS THE WORKFLOW AS A FILE THAT PUBLISHES IT, with Copy,
// and says it is not the file the author wrote.
func TestAWorkflowsDefinitionTabIsItsYAML(t *testing.T) {
	def := wf.Workflow{Slug: "daily_sales", Name: "daily_sales", Kind: wf.KindDAG, Schedule: "0 4 * * *",
		Nodes: []wf.Node{{ID: "load", Run: "python load.py"}}}
	_, body := get(t, workflowPageUI(postgres.WorkflowSummary{Slug: "daily_sales"}, def), "/workflows/daily_sales")
	at := strings.Index(body, `<section id="definition"`)
	if at < 0 {
		t.Fatal("there is no Definition tab")
	}
	tab := body[at : at+strings.Index(body[at:], "</section>")]
	for _, want := range []string{"name: daily_sales", "schedule: 0 4 * * *", "run: python load.py", `data-copy="#workflow-yaml"`, "not the file"} {
		if !strings.Contains(tab, want) {
			t.Errorf("the Definition tab has no %q", want)
		}
	}
}
