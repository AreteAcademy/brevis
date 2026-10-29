package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

func failedRunPage() RunPage {
	id := uuid.MustParse("a91f03c2-0000-4000-8000-000000000000")
	logs := []postgres.StepLog{
		{NodeID: "build", MapIndex: run.Unmapped, Status: "success", Log: "building\n"},
		{NodeID: "test", MapIndex: run.Unmapped, Attempt: 1, Status: "failed", ExitCode: exit(1),
			Log: "[WARNING]: severity warn\nError: 1 test failed\n"},
		{NodeID: "notify", MapIndex: run.Unmapped, Status: "running"},
	}
	return RunPage{
		Run:  run.Run{ID: id, WorkflowSlug: "environment_alert", Status: run.StatusFailed, CreatedAt: created},
		Logs: logs,
		Cause: &diagnosis.Cause{Step: "test", MapIndex: run.Unmapped, Attempt: 1, LineNo: 2,
			Line: "Error: 1 test failed"},
	}
}

// THE BODY IS TABS: Logs first, with how many lines; Steps; Parameters --
// tabs by the hash, so a link can open one, and stacked with no script.
func TestTheRunsBodyIsTabs(t *testing.T) {
	page := draw(t, RunPane(failedRunPage()))
	for _, want := range []string{
		`id="run-tabs" data-hash-tabs`, `data-hash-tabs-bar data-js`,
		`data-tab="logs"`, `data-tab="steps"`, `data-tab="params"`,
		`data-tab-panel="logs"`, `data-tab-panel="steps"`, `data-tab-panel="params"`,
		`#run-tabs [data-tab-panel]{display:none}`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s", want)
		}
	}
	if !strings.Contains(between(page, `data-tab="logs"`, "</button>"), ">3<") {
		t.Error("the Logs tab does not count its lines")
	}
	if strings.Index(page, `id="logs" data-tab-panel`) > strings.Index(page, `id="steps" data-tab-panel`) {
		t.Error("Logs is not the first panel")
	}
}

func between(s, from, to string) string {
	at := strings.Index(s, from)
	if at < 0 {
		return ""
	}
	end := strings.Index(s[at:], to)
	if end < 0 {
		return s[at:]
	}
	return s[at : at+end]
}

// THE LOGS TAB: each attempt under its heading, each line numbered with its
// level, the cause marked where the head's button will find it, a running
// step saying its log is coming, and the whole log to download.
func TestTheLogsTabDrawsTheRunsLog(t *testing.T) {
	p := failedRunPage()
	panel := between(draw(t, RunPane(p)), `id="logs" data-tab-panel="logs"`, `id="steps" data-tab-panel="steps"`)
	for _, want := range []string{
		"test · attempt 2", `data-n="2" data-level="warn"`, `data-n="3" data-level="err"`,
		`id="log-cause"`, "notify is running — its log arrives when it ends",
		`href="/runs/a91f03c2-0000-4000-8000-000000000000/log.txt"`,
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("the Logs tab lacks %s", want)
		}
	}
	if strings.Count(panel, `id="log-cause"`) != 1 {
		t.Error("the cause is not marked once")
	}
}

// A ten-megabyte log does not become a ten-megabyte page.
func TestAHugeLogIsABoundedPage(t *testing.T) {
	big := strings.Repeat("fetched a page of rows from the upstream api\n", 10<<20/45)
	p := failedRunPage()
	p.Logs = []postgres.StepLog{{NodeID: "load", MapIndex: run.Unmapped, Status: "failed", Log: big}}
	p.Cause = nil
	page := draw(t, RunPane(p))
	if len(page) > 1<<20 {
		t.Errorf("a %d-byte log drew a %d-byte page", len(big), len(page))
	}
	if !strings.Contains(page, "earlier lines — in the download") {
		t.Error("the lines left out are not counted")
	}
}

// THE LOG'S TOOLS ARE THE SCRIPT'S: a search, the three levels, wrapping and
// a copy of what shows -- each hidden where no script runs, since a search
// box that searches nothing is worse than none. The download is a plain link.
func TestTheLogsToolsAreThereWhereAScriptRuns(t *testing.T) {
	p := failedRunPage()
	panel := between(draw(t, RunPane(p)), `id="logs" data-tab-panel="logs"`, `id="steps" data-tab-panel="steps"`)
	if !strings.Contains(panel, `data-log data-run="a91f03c2-0000-4000-8000-000000000000"`) {
		t.Error("the log does not say whose it is")
	}
	// Each tool, or the group it sits in, carries data-js.
	for _, wrapper := range []struct{ open, tool string }{
		{`<label data-js`, `data-log-search`},
		{`<div data-js role="group"`, `data-log-level="err"`},
		{`<button type="button" data-js data-log-wrap`, `data-log-wrap`},
		{`<button type="button" data-js data-log-copy`, `data-log-copy`},
	} {
		at, tool := strings.Index(panel, wrapper.open), strings.Index(panel, wrapper.tool)
		if at < 0 || tool < at {
			t.Errorf("%s is not inside %s", wrapper.tool, wrapper.open)
		}
	}
	for _, lv := range []string{`data-log-level="all" aria-pressed="true"`, `data-log-level="warn" aria-pressed="false"`} {
		if !strings.Contains(panel, lv) {
			t.Errorf("no %s", lv)
		}
	}
	if !strings.Contains(panel, `<span data-text class="whitespace-pre">Error: 1 test failed</span>`) {
		t.Error("a line's text is not marked for the search")
	}
}

// "GO TO THE ERROR" IS ON THE CAUSE, when the cause is a line of the log --
// and only then: a cause read from the engine's error has no line to go to.
func TestWhyItFailedGoesToTheErrorInTheLog(t *testing.T) {
	head := headOf(t, failedRunPage())
	why := between(head, "data-why", "</section>")
	if !strings.Contains(why, "data-goto-error") || !strings.Contains(why, "Go to the error in the log") {
		t.Error("why it failed has no way to the error in the log")
	}
	p := failedRunPage()
	p.Cause.LineNo = 0
	if strings.Contains(between(headOf(t, p), "data-why", "</section>"), "data-goto-error") {
		t.Error("a cause with no line in the log offers to go there")
	}
}

func multiStep(t *testing.T) RunPage {
	t.Helper()
	p := failedRunPage()
	p.Run.Definition = []byte(`{"slug":"environment_alert","nodes":[{"id":"build","run":"sh -c 'echo building'"},{"id":"test","run":"sh test.sh"},{"id":"notify","action":"slack.post"}],"edges":[{"from":"build","to":"test"},{"from":"test","to":"notify"}]}`)
	p.Graph = `{"nodes":[]}`
	return p
}

// THE GRAPH IS THE STEPS TAB'S, and lives outside the live body: the refresh
// redraws the body every time a step ends, and a React root inside it would
// be torn out from under itself. Its scripts come with the tab, not the page.
func TestTheGraphIsInTheStepsTabAndLoadsWithIt(t *testing.T) {
	page := draw(t, Runs(RunsScreen{Detail: ptr(multiStep(t))}))
	graph := between(page, "data-run-graph", "</div>")
	if !strings.Contains(page, `data-tab-panel="steps" data-run-graph`) || !strings.Contains(graph, `data-island-js=`) || !strings.Contains(graph, "dag.js") {
		t.Errorf("the graph is not a Steps panel carrying its scripts: %.300s", graph)
	}
	body := between(page, `id="run-body"`, `id="run-pane-end"`)
	if strings.Contains(body, `id="dag"`) {
		t.Error("the graph is inside the live body")
	}
	if strings.Contains(page, "react-dom.js\"></script>") || strings.Contains(page, "<script src=\"/assets/dag.js") {
		t.Error("the page loads the graph's scripts before its tab opens")
	}
}

func ptr(p RunPage) *RunPage { return &p }

// THE STEPS TABLE SAYS WHAT EACH STEP RAN, where it said what it published:
// that is the Published context tab's now, whole.
func TestTheStepsTableSaysTheCommand(t *testing.T) {
	page := draw(t, RunPane(multiStep(t)))
	table := between(page, "data-step-table", `id="params"`)
	for _, want := range []string{">Command<", "sh test.sh", "sh -c &#39;echo building&#39;"} {
		if !strings.Contains(table, want) {
			t.Errorf("the steps table lacks %s", want)
		}
	}
	if strings.Contains(table, ">Output<") || strings.Contains(table, "no output recorded") {
		t.Error("the table still carries what the steps published")
	}
}

// PUBLISHED CONTEXT, PER STEP: the JSON, formatted, with its size -- or why
// there is none: still running, failed before publishing, published nothing.
func TestThePublishedContextTab(t *testing.T) {
	p := multiStep(t)
	p.Logs[0].Output = []byte(`{"rows":1284,"target":"bronze.inpe"}`)
	tab := between(draw(t, RunPane(p)), `id="context" data-tab-panel="context"`, "</section>")
	for _, want := range []string{
		"build", "&#34;rows&#34;: 1284", "36 bytes",
		"test", "Failed before publishing",
		"notify", "Still running",
	} {
		if !strings.Contains(tab, want) {
			t.Errorf("the context tab lacks %s", want)
		}
	}
	p.Logs[0].Output = nil
	if !strings.Contains(between(draw(t, RunPane(p)), `id="context" data-tab-panel="context"`, "</section>"), "Published nothing") {
		t.Error("a step that published nothing does not say so")
	}
}

// THE PARAMETERS TAB: the auto params, each with what it means in the
// reader's language, and the values this run was given.
func TestTheParametersTabExplainsAndLists(t *testing.T) {
	p := multiStep(t)
	p.Run.Params = map[string]string{"load_full": "true", "region": "br"}
	tab := between(drawIn(t, "pt-BR", RunPane(p)), `id="params" data-tab-panel="params"`, `id="context"`)
	for _, want := range []string{"adjusted_at", "o relógio a usar no lugar de now()", "load_full", "true", "region", "br"} {
		if !strings.Contains(tab, want) {
			t.Errorf("the parameters tab lacks %s", want)
		}
	}
	p.Run.Params = nil
	if !strings.Contains(between(draw(t, RunPane(p)), `id="params" data-tab-panel="params"`, `id="context"`), "This run was given no parameters") {
		t.Error("a run with no parameters does not say so")
	}
}

func drawIn(t *testing.T, lang string, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(i18n.WithLang(context.Background(), lang), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
