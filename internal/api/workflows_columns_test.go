package api_test

import (
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// rowOf is the table row of a workflow, from <tr to </tr>.
func rowOf(t *testing.T, body, slug string) string {
	t.Helper()
	at := strings.Index(body, `data-href="/workflows/`+slug+`"`)
	if at < 0 {
		t.Fatalf("no clickable row for %s", slug)
	}
	start := strings.LastIndex(body[:at], "<tr")
	return body[start : at+strings.Index(body[at:], "</tr>")]
}

func columnsUI(n *int) *api.UI {
	reads := readsFake{
		workflows: []postgres.WorkflowSummary{
			{Slug: "polyglot", Name: "polyglot", Project: "zarv", HasSchedule: true, Active: true, Cron: "0 9 * * *", LastStatus: "success"},
			{Slug: "busy", Name: "busy", Project: "zarv", HasSchedule: true, Active: true, Cron: "*/5 * * * *", LastStatus: "queued"},
			{Slug: "resting", Name: "resting", Project: "zarv", HasSchedule: true, Cron: "0 3 * * *", LastStatus: "success"},
		},
		connects: map[string][]string{"polyglot": {"bigquery", "gs", "dbt"}},
		recentN:  n,
	}
	one, every := 0, 0
	defs := mapDefs{one: &one, every: &every, defs: map[string]wf.Workflow{
		"polyglot": {Slug: "polyglot",
			Params:  []wf.Param{{Name: "max_ocr", Type: wf.ParamInteger, Uses: "vision"}, {Name: "full", Type: wf.ParamBool, Uses: "bigquery"}},
			Trigger: wf.Trigger{OnLanded: []string{"pubsub://acme/orders"}},
			Nodes: []wf.Node{
				{ID: "fetch", Run: "go run ./cmd/fetch"}, {ID: "ocr", Run: "python ocr.py"},
				{ID: "geo", Run: "node geo.js"}, {ID: "silver", Run: "dbt build --select silver+"},
				{ID: "x", Run: "bash x.sh"},
			}},
		"busy":    {Slug: "busy", Nodes: []wf.Node{{ID: "a", Run: "python a.py"}}},
		"resting": {Slug: "resting", Nodes: []wf.Node{{ID: "a", Run: "python a.py"}, {ID: "b", Run: "python b.py"}}},
	}}
	return api.NewUI(reads, defs, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler)).
		WithFailing(failingSlugs{"polyglot"})
}

var logoRE = regexp.MustCompile(`role="img" aria-label="([^"]+)"`)

// logosIn is the logos a piece of markup draws, by name, in order.
func logosIn(html string) []string {
	var out []string
	for _, m := range logoRE.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

// cell is a row's cell that carries the data attribute.
func cell(t *testing.T, row, attr string) string {
	t.Helper()
	at := strings.Index(row, attr)
	if at < 0 {
		t.Fatalf("the row has no %s cell", attr)
	}
	return row[at : at+strings.Index(row[at:], "</td>")]
}

// A ROW SAYS WHAT IT RUNS, up to four marks and the first two by name, and
// WHAT IT TALKS TO -- what it wrote, what its parameters bill, what lands to
// start it -- once each, and never a language again.
func TestAWorkflowsRowSaysWhatItRunsAndTalksTo(t *testing.T) {
	_, body := get(t, columnsUI(nil), "/workflows")
	row := rowOf(t, body, "polyglot")

	langs := cell(t, row, "data-col-lang")
	if got := logosIn(langs); len(got) != 4 {
		t.Errorf("the language cell draws %v, want four marks", got)
	}
	if !regexp.MustCompile(`Go \+ Python \+\d`).MatchString(langs) {
		t.Errorf("the language cell does not name the first two and count the rest:\n%s", langs)
	}

	talks := logosIn(cell(t, row, "data-col-connects"))
	for _, want := range []string{"Google BigQuery", "Google Cloud Storage", "Google Pub/Sub", "Google Cloud"} {
		n := 0
		for _, got := range talks {
			if got == want {
				n++
			}
		}
		if n != 1 {
			t.Errorf("Connects draws %v, want %s once", talks, want)
		}
	}
	if strings.Contains(strings.Join(talks, "|"), "dbt") {
		t.Errorf("Connects repeats a language: %v", talks)
	}
}

// THE ROW SAYS ITS STATE IN A WORD where it needs one -- failing, running --
// and its size: the project and how many steps.
func TestAWorkflowsRowSaysItsStateAndSize(t *testing.T) {
	_, body := get(t, columnsUI(nil), "/workflows")
	if row := rowOf(t, body, "polyglot"); !strings.Contains(row, "data-pill-failing") || !strings.Contains(row, "5 steps") {
		t.Error("a failing workflow of five steps does not say so")
	}
	busy := rowOf(t, body, "busy")
	if !strings.Contains(busy, "data-pill-running") || !strings.Contains(cell(t, busy, "data-col-next"), "running") {
		t.Error("a running workflow does not say so, in its name and in its next run")
	}
}

// A PAUSED ROW IS QUIETER, and still a row: its data fades, its switch does
// not.
func TestAPausedWorkflowsRowFades(t *testing.T) {
	_, body := get(t, columnsUI(nil), "/workflows")
	row := rowOf(t, body, "resting")
	if !strings.Contains(row, "data-paused") {
		t.Error("a paused row is drawn like an active one")
	}
	if !strings.Contains(cell(t, row, "data-col-schedule"), "paused") {
		t.Error("a paused schedule does not say it is paused")
	}
}

// THE STRIP IS THE LAST TWENTY RUNS.
func TestTheWorkflowsStripIsTheLastTwenty(t *testing.T) {
	n := 0
	get(t, columnsUI(&n), "/workflows")
	if n != 20 {
		t.Errorf("the strip asked for %d runs, want 20", n)
	}
}

// AN EMPTY LIST SAYS WHAT TO DO, with a link that clears every filter.
func TestAnEmptyWorkflowsListClearsTheFilters(t *testing.T) {
	_, body := answerGet(t, columnsUI(nil), "/workflows?q=nothing&tag=x")
	at := strings.Index(body, "No workflow found")
	if at < 0 {
		t.Fatal("an empty list does not say so")
	}
	if !strings.Contains(body[at:], `href="/workflows"`) {
		t.Error("the empty list has no link that clears the filters")
	}
}
