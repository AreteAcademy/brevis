package api_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func workflowsUI() *api.UI {
	reads := readsFake{workflows: []postgres.WorkflowSummary{
		{Slug: "daily_sales", Name: "daily_sales", Project: "analytics", LastStatus: "success", HasSchedule: true, Active: true},
		{Slug: "daily_refunds", Name: "daily_refunds", Project: "analytics", LastStatus: "failed", HasSchedule: true},
		{Slug: "hourly_clicks", Name: "hourly_clicks", Project: "analytics", LastStatus: "success", HasSchedule: true, Active: true},
		{Slug: "backfill", Name: "backfill", Project: "analytics", LastStatus: "running"},
	}}
	return api.NewUI(reads, nil, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

func answerGet(t *testing.T, ui *api.UI, path string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Brevis-Fragment", "answer")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// THE LIST ANSWERS WHILE SOMEBODY TYPES, AND THE SERVER STILL DECIDES. The list
// is paginated on the server, so hiding the rows of one page would search a
// page and not the workflows: the search box submits its GET form, and the
// handler draws the list alone -- filters, count, table, pages -- for ui.js to
// swap in under the box, which keeps its focus.
func TestTheWorkflowsListIsAFragmentForTheSearchBox(t *testing.T) {
	code, body := answerGet(t, workflowsUI(), "/workflows?q=daily")
	if code != http.StatusOK {
		t.Fatalf("the fragment answered %d", code)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, `name="q"`) {
		t.Error("the fragment carries the page or the search box, and swapping it would steal the focus")
	}
	if !strings.Contains(body, "daily_sales") || !strings.Contains(body, "daily_refunds") || strings.Contains(body, "hourly_clicks") {
		t.Error("the fragment does not answer the search")
	}

	_, page := get(t, workflowsUI(), "/workflows?q=daily")
	if !strings.Contains(page, `data-swap="workflows-list"`) || !strings.Contains(page, `id="workflows-list"`) {
		t.Error("the search form does not name the list it swaps")
	}
	if !strings.Contains(page, "data-swap-typing") {
		t.Error("the search form does not answer while somebody types")
	}
	if regexp.MustCompile(`>\s*Search\s*</button>`).MatchString(page) {
		t.Error("the Search button is still there")
	}
	_, js := get(t, workflowsUI(), "/assets/ui.js")
	if !strings.Contains(strip(js), `form[data-swap-typing]`) {
		t.Error("ui.js does not submit a typing form")
	}
}

// STATE AND ACTIVE ARE SEGMENTED, EACH CHOICE WITH ITS COUNT: how many rows it
// would show with the rest of the filter as it is. The count ignores its own
// filter -- "Failure 1" while "Success" is chosen -- or every other choice
// would read 0.
func TestTheWorkflowsFiltersCountWhatEachChoiceWouldShow(t *testing.T) {
	_, body := get(t, workflowsUI(), "/workflows?q=daily&state=success")
	for _, want := range []string{`aria-label="State"`, `aria-label="Schedule"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the filters have no %s", want)
		}
	}
	for _, s := range []struct{ label, count string }{
		{"Any state", "2"}, {"Success", "1"}, {"Failure", "1"},
		{"Any schedule", "1"}, {"Active", "1"}, {"Paused", "0"},
	} {
		if !segment(s.label, s.count).MatchString(body) {
			t.Errorf("the filters do not say %s %s", s.label, s.count)
		}
	}
	if !strings.Contains(body, `href="/workflows?q=daily&amp;state=failed"`) {
		t.Error("a state choice drops the search")
	}
}

// segment is a Segmented choice's label and count, as the markup prints them.
func segment(label, count string) *regexp.Regexp {
	return regexp.MustCompile(`>\s*` + regexp.QuoteMeta(label) + `\s*<span[^>]*>` + count + `</span>`)
}
