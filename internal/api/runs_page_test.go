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
	// listed keeps the filter the page was read with; total stands in for a
	// longer list than the page holds.
	listed *postgres.RunFilter
	total  int
}

func (f runsReads) Runs(_ context.Context, q postgres.RunFilter) ([]postgres.RunSummary, error) {
	if f.listed != nil {
		*f.listed = q
	}
	return f.runs, nil
}
func (f runsReads) CountRuns(context.Context, postgres.RunFilter) (int, error) {
	if f.total > 0 {
		return f.total, nil
	}
	return len(f.runs), nil
}
func (f runsReads) CountRunsByState(_ context.Context, q postgres.RunFilter) (map[string]int, error) {
	// The sidebar badge counts with no filter at all; only the list's is recorded.
	if f.asked != nil && q.Workflow != "" {
		*f.asked = q
	}
	return f.counts, nil
}

func dur(d time.Duration) *time.Duration { return &d }

func runsUI(reads runsReads) *api.UI {
	return api.NewUI(reads, nil, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

// A RETRY SAYS SO: `attempt` counts from 0, so the second try is ↻2, its
// meaning in the tooltip, and a first try says nothing.
func TestARunsItemMarksARetry(t *testing.T) {
	ui := runsUI(runsReads{runs: []postgres.RunSummary{
		{ID: "a", WorkflowSlug: "slow", Status: "success", Duration: dur(4 * time.Minute)},
		{ID: "b", WorkflowSlug: "half", Status: "failed", Duration: dur(2 * time.Minute), Attempt: 1},
	}})
	_, body := get(t, ui, "/runs")
	if strings.Count(body, "↻2") != 1 || !strings.Contains(body, `title="attempt 2 of this run"`) || strings.Contains(body, "↻1") {
		t.Error("the retry is not marked once as ↻2")
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
// its counts, the runs and the pages -- for ui.js to swap every few seconds.
// Each item says which run it is, so a new one can be told from the rest.
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
