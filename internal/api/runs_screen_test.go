package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// screenExecs answers Get for any of a set of runs, and nothing for the rest.
type screenExecs struct {
	execsFake
	runs map[uuid.UUID]dom.Run
}

func (e screenExecs) Get(_ context.Context, id uuid.UUID) (dom.Run, error) {
	if r, ok := e.runs[id]; ok {
		return r, nil
	}
	return dom.Run{}, pgx.ErrNoRows
}

var (
	newest = uuid.MustParse("11111111-0000-0000-0000-000000000001")
	older  = uuid.MustParse("11111111-0000-0000-0000-000000000002")
	beyond = uuid.MustParse("11111111-0000-0000-0000-000000000003")
)

func screenUI() *api.UI {
	slot := time.Date(2026, 10, 26, 22, 18, 0, 0, time.UTC)
	reads := runsReads{runs: []postgres.RunSummary{
		{ID: newest.String(), WorkflowSlug: "vendors_fire_inpe", Status: "running", LogicalDate: &slot, CreatedAt: time.Now()},
		{ID: older.String(), WorkflowSlug: "environment_alert", Status: "failed", LogicalDate: &slot,
			CreatedAt: time.Now().Add(-time.Minute), Duration: dur(12200 * time.Millisecond)},
	}}
	execs := screenExecs{runs: map[uuid.UUID]dom.Run{
		newest: {ID: newest, WorkflowSlug: "vendors_fire_inpe", Status: dom.StatusRunning},
		older:  {ID: older, WorkflowSlug: "environment_alert", Status: dom.StatusFailed},
		beyond: {ID: beyond, WorkflowSlug: "id_analytics", Status: dom.StatusSuccess},
	}}
	return api.NewUI(reads, nil, execs, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

// selected is the run the list marks, and detail the one the right side shows.
func selected(body string) (list, detail string) {
	if m := regexp.MustCompile(`data-run-id="([^"]+)"[^>]*aria-current="true"`).FindStringSubmatch(body); m != nil {
		list = m[1]
	}
	if m := regexp.MustCompile(`data-run-detail="([^"]+)"`).FindStringSubmatch(body); m != nil {
		detail = m[1]
	}
	return list, detail
}

// ONE SCREEN, TWO ADDRESSES. /runs is the list with the newest run open; the
// list is a column beside it, and each pane scrolls on its own.
func TestTheRunsScreenOpensTheNewestRun(t *testing.T) {
	code, body := get(t, screenUI(), "/runs")
	if code != http.StatusOK {
		t.Fatalf("/runs answered %d", code)
	}
	if !strings.Contains(body, "data-fill") || !strings.Contains(body, "data-runs-list") {
		t.Fatal("the runs screen is not a list column filling the shell")
	}
	if list, detail := selected(body); list != newest.String() || detail != newest.String() {
		t.Errorf("selected %q in the list and %q in the detail; wanted the newest, %s", list, detail, newest)
	}
}

// /runs/{id} is the same screen with that run open -- the address alerts and
// old links carry.
func TestARunsAddressOpensThatRun(t *testing.T) {
	_, body := get(t, screenUI(), "/runs/"+older.String())
	if !strings.Contains(body, "data-runs-list") {
		t.Fatal("the run's address lost the list")
	}
	if list, detail := selected(body); list != older.String() || detail != older.String() {
		t.Errorf("selected %q in the list and %q in the detail; wanted %s", list, detail, older)
	}
	if !strings.Contains(body, `href="/runs/`+older.String()+`"`) {
		t.Error("a list item is not a link to its run")
	}
}

// A run that is not on this page of the list is still the one opened: an
// alert from last week has to land on its run, not on whatever ran last.
func TestARunOffTheListIsStillOpened(t *testing.T) {
	code, body := get(t, screenUI(), "/runs/"+beyond.String())
	if code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if list, detail := selected(body); list != "" || detail != beyond.String() {
		t.Errorf("selected %q in the list and %q in the detail; wanted nothing and %s", list, detail, beyond)
	}
}

func TestAnUnknownRunIsNotFound(t *testing.T) {
	if code, _ := get(t, screenUI(), "/runs/"+uuid.NewString()); code != http.StatusNotFound {
		t.Errorf("answered %d, wanted 404", code)
	}
}

func TestNoRunsIsAnEmptyScreen(t *testing.T) {
	_, body := get(t, runsUI(runsReads{}), "/runs")
	if _, detail := selected(body); detail != "" {
		t.Errorf("a run is open with no runs: %s", detail)
	}
	if !strings.Contains(body, "data-runs-list") {
		t.Error("the empty screen lost the list column")
	}
}

// AN ITEM IS A LINE TO SCAN: the state's shape, the workflow, how long ago,
// the slot's hour and how long it took -- or that it is still running.
func TestARunsItemSaysWhatAndWhen(t *testing.T) {
	_, body := get(t, screenUI(), "/runs")
	item := func(id uuid.UUID) string {
		at := strings.Index(body, `data-run-id="`+id.String()+`"`)
		if at < 0 {
			t.Fatalf("no item for %s", id)
		}
		end := strings.Index(body[at:], "</a>")
		return body[at : at+end]
	}
	running, failed := item(newest), item(older)
	for _, want := range []string{"vendors_fire_inpe", "22:18", "running…"} {
		if !strings.Contains(running, want) {
			t.Errorf("the running item lacks %q:\n%s", want, running)
		}
	}
	for _, want := range []string{"environment_alert", "22:18", "12.2s", "1 min ago"} {
		if !strings.Contains(failed, want) {
			t.Errorf("the failed item lacks %q:\n%s", want, failed)
		}
	}
	if !strings.Contains(failed, `data-status="failed"`) {
		t.Errorf("the failed item has no state mark:\n%s", failed)
	}
}

// THREE CHOICES, BECAUSE THERE ARE THREE QUESTIONS: everything, what broke,
// what is still going. "Running" is in flight -- queued and retrying too --
// and each count is read with the rest of the filter and without the state.
func TestTheRunsSegmentIsAllFailedRunning(t *testing.T) {
	var asked postgres.RunFilter
	ui := runsUI(runsReads{
		counts: map[string]int{"success": 7, "failed": 2, "running": 1, "queued": 2, "retrying": 1},
		asked:  &asked,
	})
	_, body := get(t, ui, "/runs?workflow=fire&state=failed")
	if asked.State != "" || len(asked.States) != 0 || asked.Workflow != "fire" {
		t.Errorf("the counts were read with %+v", asked)
	}
	for _, s := range []struct{ label, count string }{{"All", "13"}, {"Failed", "2"}, {"Running", "4"}} {
		if !segment(s.label, s.count).MatchString(body) {
			t.Errorf("the segment does not say %s %s", s.label, s.count)
		}
	}
	for _, gone := range []string{">Success<", ">Queued<", "Any state"} {
		if strings.Contains(body, gone) {
			t.Errorf("the segment still offers %s", gone)
		}
	}
	if !strings.Contains(body, `href="/runs?state=running&amp;workflow=fire"`) {
		t.Error("Running does not keep the search")
	}
}

func TestRunningMeansInFlight(t *testing.T) {
	var listed postgres.RunFilter
	_, _ = get(t, runsUI(runsReads{listed: &listed}), "/runs?state=running")
	if listed.State != "" || strings.Join(listed.States, ",") != "running,queued,retrying" {
		t.Errorf("Running listed %+v", listed)
	}
}

// A state the segment does not offer -- a link from the Overview to the
// canceled ones, say -- still filters, and says so where it can be taken off.
func TestAnotherStateIsAFilterYouCanRemove(t *testing.T) {
	var listed postgres.RunFilter
	_, body := get(t, runsUI(runsReads{listed: &listed}), "/runs?state=canceled")
	if listed.State != "canceled" {
		t.Errorf("listed %+v", listed)
	}
	if !strings.Contains(body, ">canceled<") || !strings.Contains(body, `href="/runs" class="text-muted hover:text-ink" aria-label="Remove this filter"`) {
		t.Error("the canceled filter is not shown with its ×")
	}
}

// THE SEARCH IS A FORM: it works with no script, `/` reaches it, and it keeps
// the state that was chosen.
func TestTheRunsSearchIsAFormOnTheWorkflow(t *testing.T) {
	_, body := get(t, runsUI(runsReads{}), "/runs?state=failed&workflow=fire")
	form := regexp.MustCompile(`(?s)<form[^>]*method="get"[^>]*action="/runs"[^>]*>.*?</form>`).FindString(body)
	if form == "" {
		t.Fatal("no search form")
	}
	for _, want := range []string{`name="workflow"`, `value="fire"`, `data-hotkey="/"`, `type="hidden" name="state" value="failed"`} {
		if !strings.Contains(form, want) {
			t.Errorf("the search lacks %s", want)
		}
	}
}

// PAGES ARE OLDER AND NEWER: a column has no room for page numbers, and the
// list is read from the top.
func TestTheRunsColumnPagesOlderAndNewer(t *testing.T) {
	runs := make([]postgres.RunSummary, 25)
	for i := range runs {
		runs[i] = postgres.RunSummary{ID: uuid.NewString(), WorkflowSlug: "w", Status: "success", CreatedAt: time.Now()}
	}
	_, body := get(t, runsUI(runsReads{runs: runs, total: 60}), "/runs?page=2&workflow=w")
	for _, want := range []string{`href="/runs?workflow=w"`, `href="/runs?page=3&amp;workflow=w"`, ">Newer<", ">Older<", "26–50 of 60"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pages lack %s", want)
		}
	}
	_, last := get(t, runsUI(runsReads{runs: runs[:10], total: 60}), "/runs?page=3")
	if strings.Contains(last, `href="/runs?page=4"`) {
		t.Error("the last page offers an older one")
	}
}

// A filter that finds nothing says so -- and not "no run yet", which would
// read as an empty installation.
func TestASearchThatFindsNothingSaysSo(t *testing.T) {
	_, body := get(t, runsUI(runsReads{}), "/runs?workflow=nope")
	if !strings.Contains(body, "No runs") || !strings.Contains(body, "adjust the filter or the search") {
		t.Error("an empty search does not say what to do")
	}
	if strings.Contains(body, "No run yet") {
		t.Error("an empty search reads as an empty installation")
	}
}

// AN ITEM CARRIES WHAT ITS WORKFLOW RUNS IN: the tool first (dbt), then the
// language, two at most -- from the definition, read once per workflow.
func TestARunsItemCarriesItsWorkflowsLogos(t *testing.T) {
	def := wf.Workflow{Slug: "models", Nodes: []wf.Node{
		{ID: "build", Run: "dbt build"}, {ID: "fetch", Run: "python fetch.py"}, {ID: "x", Run: "node x.js"},
	}}
	calls := 0
	ui := api.NewUI(runsReads{runs: []postgres.RunSummary{
		{ID: uuid.NewString(), WorkflowSlug: "models", Status: "success", CreatedAt: time.Now()},
		{ID: uuid.NewString(), WorkflowSlug: "models", Status: "failed", CreatedAt: time.Now()},
	}}, countingDefs{w: def, calls: &calls}, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
	_, body := get(t, ui, "/runs")
	nav := body[strings.Index(body, `aria-label="Runs"`):]
	item := nav[:strings.Index(nav, "</a>")]
	for _, want := range []string{`aria-label="dbt"`, `aria-label="Python"`} {
		if !strings.Contains(item, want) {
			t.Errorf("the item has no %s logo:\n%s", want, item)
		}
	}
	if strings.Contains(item, `aria-label="Node.js"`) {
		t.Error("a third logo")
	}
	if calls != 1 {
		t.Errorf("the definition was read %d times for one workflow", calls)
	}
}

type countingDefs struct {
	w     wf.Workflow
	calls *int
}

func (d countingDefs) Definition(context.Context, string) (wf.Workflow, error) {
	*d.calls++
	return d.w, nil
}

func detailGet(t *testing.T, ui *api.UI, path string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Brevis-Fragment", "detail")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// OPENING ANOTHER RUN SWAPS THE PANE, NOT THE PAGE. The same address with
// X-Brevis-Fragment: detail answers the open run alone -- no layout, no list
// -- carrying the breadcrumb's last item for ui.js to write in the bar.
func TestARunsDetailIsAFragment(t *testing.T) {
	code, frag := detailGet(t, screenUI(), "/runs/"+older.String())
	if code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if strings.Contains(frag, "<html") || strings.Contains(frag, "data-runs-list") {
		t.Error("the fragment carries the page or the list")
	}
	if _, detail := selected(frag); detail != older.String() {
		t.Errorf("the fragment opens %q", detail)
	}
	if !strings.Contains(frag, `data-crumb="environment_alert · 11111111"`) {
		t.Error("the fragment does not carry its breadcrumb")
	}
	if code, _ := detailGet(t, screenUI(), "/runs/"+uuid.NewString()); code != http.StatusNotFound {
		t.Errorf("an unknown run's fragment answered %d", code)
	}
}

// The page is ready for the swap: the pane has an id, the bar's last crumb a
// marker, and the scripts know the fragment, the history and the graph.
func TestTheRunsScreenIsReadyToSwap(t *testing.T) {
	ui := screenUI()
	_, page := get(t, ui, "/runs")
	for _, want := range []string{`id="run-pane"`, `data-crumb-current`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %s", want)
		}
	}
	_, js := get(t, ui, "/assets/ui.js")
	for _, want := range []string{`"X-Brevis-Fragment": "detail"`, "pushState", "popstate", "BrevisDag", "ArrowDown"} {
		if !strings.Contains(strip(js), want) {
			t.Errorf("ui.js has no %s", want)
		}
	}
	if strings.Contains(js, "tr[data-run-id]") {
		t.Error("the Live list still looks for table rows")
	}
	_, dag := get(t, ui, "/assets/dag.js")
	if !strings.Contains(dag, "window.BrevisDag") {
		t.Error("dag.js does not expose mount and unmount")
	}
}
