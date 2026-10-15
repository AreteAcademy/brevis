package api_test

import (
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

func workflowsUI() *api.UI {
	reads := readsFake{workflows: []postgres.WorkflowSummary{
		{Slug: "daily_sales", Name: "daily_sales", Project: "analytics", LastStatus: "success", HasSchedule: true, Active: true, Tags: []string{"finance", "sales"}},
		{Slug: "daily_refunds", Name: "daily_refunds", Project: "analytics", LastStatus: "failed", HasSchedule: true, Tags: []string{"finance"}},
		{Slug: "hourly_clicks", Name: "hourly_clicks", Project: "analytics", LastStatus: "success", HasSchedule: true, Active: true, Tags: []string{"sales"}},
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
	if strings.Contains(body, "<html") || strings.Contains(body, `aria-label="Search workflows"`) {
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

// TWO TAGS NARROW TO THE WORKFLOWS CARRYING BOTH, and the URL carries the
// set: one `tag=` each, so the filter can be pasted.
func TestTwoTagsNarrowToTheWorkflowsCarryingBoth(t *testing.T) {
	_, body := answerGet(t, workflowsUI(), "/workflows?tag=finance&tag=sales")
	if !strings.Contains(body, "daily_sales") || strings.Contains(body, "daily_refunds") || strings.Contains(body, "hourly_clicks") {
		t.Error("two tags did not narrow to the workflow carrying both")
	}
}

// THE TAGS ARE A POPOVER, NOT A WALL OF CHIPS: a <details> holding a GET form
// -- the rest of the filter as hidden fields, a box that filters the tag
// list, one checkbox per tag -- so it works with no script. The chosen tags
// show beside it as chips, each taking out its own tag.
func TestTheTagsAreAPopoverWithTheChosenAsChips(t *testing.T) {
	_, body := get(t, workflowsUI(), "/workflows?q=daily&tag=finance&tag=sales")
	at := strings.Index(body, "<details data-popover")
	if at < 0 {
		t.Fatal("the tags are not a popover")
	}
	pop := body[at : at+strings.Index(body[at:], "</details>")]
	if !regexp.MustCompile(`>\s*Tags\s*<span[^>]*>2</span>`).MatchString(pop) {
		t.Error("the popover's button does not say how many tags are chosen")
	}
	for _, want := range []string{
		`<form method="get" action="/workflows"`,
		`type="hidden" name="q" value="daily"`,
		`data-submit-on-change`,
		`data-filter-for="workflow-tags"`,
	} {
		if !strings.Contains(pop, want) {
			t.Errorf("the popover has no %s", want)
		}
	}
	for _, tag := range []string{"finance", "sales"} {
		if !regexp.MustCompile(`name="tag" value="` + tag + `" checked`).MatchString(pop) {
			t.Errorf("the chosen tag %s is not checked", tag)
		}
	}
	if strings.Contains(pop, `name="tag" value="finance"`) && strings.Contains(pop, `type="hidden" name="tag"`) {
		t.Error("the popover carries the tags as hidden fields, so unchecking one keeps it")
	}
	if !strings.Contains(body, `href="/workflows?q=daily&amp;tag=sales"`) {
		t.Error("the finance chip does not take out finance alone")
	}

	_, js := get(t, workflowsUI(), "/assets/ui.js")
	if !strings.Contains(strip(js), `form[data-submit-on-change]`) {
		t.Error("ui.js does not submit the popover when a box is checked")
	}
}

// THE LAST RUNS ARE A STRIP OF SQUARES, oldest to newest, each a link to its
// run that says its state and time -- read in ONE query for the whole page.
func TestEachRowDrawsItsLastRunsFromOneQuery(t *testing.T) {
	calls := 0
	at := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	last, lastAt := "11111111-0000-0000-0000-000000000002", at.Add(24*time.Hour)
	reads := readsFake{
		workflows: []postgres.WorkflowSummary{
			{Slug: "daily_sales", Name: "daily_sales", Project: "analytics", Cron: "0 4 * * *",
				LastRunID: &last, LastRunAt: &lastAt, LastStatus: "failed", TotalRuns: 2},
			{Slug: "odd_one", Name: "odd_one", Project: "analytics", Cron: "0 4 * 1 *"},
			{Slug: "manual", Name: "manual", Project: "analytics"},
		},
		recent: map[string][]postgres.RecentRun{"daily_sales": {
			{ID: "11111111-0000-0000-0000-000000000001", Status: "success", At: at},
			{ID: "11111111-0000-0000-0000-000000000002", Status: "failed", At: at.Add(24 * time.Hour)},
		}},
		recentCalls: &calls,
	}
	ui := api.NewUI(reads, nil, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
	_, body := get(t, ui, "/workflows")
	if calls != 1 {
		t.Errorf("the page read the last runs %d times, want once", calls)
	}
	first := strings.Index(body, `href="/runs/11111111-0000-0000-0000-000000000001"`)
	second := strings.Index(body, `href="/runs/11111111-0000-0000-0000-000000000002"`)
	if first < 0 || second < 0 || first > second {
		t.Fatal("the strip does not link each run, oldest first")
	}
	if !strings.Contains(body, `data-recent-runs`) || !strings.Contains(body, `title="failed · 2026-10-16 04:00:00 UTC"`) {
		t.Error("a square does not say its state and time")
	}

	// Cron in words under the expression, and only where the words are sure.
	if !strings.Contains(body, "every day at 04:00") {
		t.Error("a daily schedule is not said in words")
	}
	if strings.Count(body, `data-cron-text`) != 1 {
		t.Error("a schedule the translator refuses got words anyway")
	}
}
