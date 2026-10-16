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

func rowsGet(t *testing.T, ui *api.UI, path string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Brevis-Fragment", "rows")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// THE LIST CAN BE FOLLOWED LIVE. The page carries a "Live" switch, hidden
// until ui.js can drive it, and the list sits in one region; the same URL
// with X-Brevis-Fragment: rows answers that region alone -- the filter with
// its counts, the table, the pages and the error dialogs a new failure needs
// -- for ui.js to swap every few seconds. Each row says which run it is, so a
// new one can be told from the rest.
func TestTheRunsListCanBeFollowedLive(t *testing.T) {
	ui := runsUI(runsReads{
		runs: []postgres.RunSummary{
			{ID: "11111111-0000-0000-0000-000000000001", WorkflowSlug: "daily", Status: "failed", Err: "boom"},
		},
		counts: map[string]int{"failed": 1},
	})
	_, page := get(t, ui, "/runs?workflow=daily")
	if !strings.Contains(page, `id="runs-list"`) {
		t.Fatal("the list is not one region")
	}
	// Only where a script runs: data-js, shown under <html class="js">,
	// which the head sets before the first paint (nav-latency N6).
	if !regexp.MustCompile(`<span[^>]*data-js[^>]*>\s*<button[^>]*data-live-list="runs-list"`).MatchString(page) {
		t.Error("there is no Live switch, shown only where a script runs")
	}

	code, frag := rowsGet(t, ui, "/runs?workflow=daily")
	if code != http.StatusOK {
		t.Fatalf("the fragment answered %d", code)
	}
	if strings.Contains(frag, "<html") || strings.Contains(frag, "data-live-list") {
		t.Error("the fragment carries the page or the switch")
	}
	for _, want := range []string{
		`aria-label="State"`, `data-run-id="11111111-0000-0000-0000-000000000001"`,
		`id="erro-11111111-0000-0000-0000-000000000001"`,
	} {
		if !strings.Contains(frag, want) {
			t.Errorf("the fragment has no %s", want)
		}
	}

	_, js := get(t, ui, "/assets/ui.js")
	for _, want := range []string{"data-live-list", `"X-Brevis-Fragment": "rows"`, "localStorage", "visibilitychange"} {
		if !strings.Contains(strip(js), want) {
			t.Errorf("ui.js has no %s", want)
		}
	}
}
