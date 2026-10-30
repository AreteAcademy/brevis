package api_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// failingSlugs is the failing-workflows reader, naming who is failing.
type failingSlugs []string

func (f failingSlugs) FailingWorkflows(context.Context) ([]postgres.FailingWorkflow, error) {
	out := make([]postgres.FailingWorkflow, len(f))
	for i, s := range f {
		out[i] = postgres.FailingWorkflow{Slug: s, RunID: uuid.New(), Streak: 1}
	}
	return out, nil
}

func (failingSlugs) FailureStreak(context.Context, string) (int, error) { return 1, nil }

// fiveUI is one workflow per question the state segment asks, plus one that
// answers none of them: failing (its last finished run failed, though a new
// one is queued), running, paused, manual only, and fine.
func fiveUI() *api.UI {
	reads := readsFake{workflows: []postgres.WorkflowSummary{
		{Slug: "broken", Name: "broken", Project: "p", LastStatus: "queued", HasSchedule: true, Active: true, Tags: []string{"finance"}},
		{Slug: "busy", Name: "busy", Project: "p", LastStatus: "running", HasSchedule: true, Active: true},
		{Slug: "resting", Name: "resting", Project: "p", LastStatus: "success", HasSchedule: true},
		{Slug: "handmade", Name: "handmade", Project: "p", LastStatus: "success"},
		{Slug: "fine", Name: "fine", Project: "p", LastStatus: "success", HasSchedule: true, Active: true},
	}}
	one, every := 0, 0
	defs := mapDefs{one: &one, every: &every, defs: map[string]wf.Workflow{
		"broken":   {Slug: "broken", Nodes: []wf.Node{{ID: "a", Run: "python fetch.py"}}},
		"busy":     {Slug: "busy", Nodes: []wf.Node{{ID: "a", Run: "dbt build --select silver+"}}},
		"resting":  {Slug: "resting", Nodes: []wf.Node{{ID: "a", Run: "python load.py"}, {ID: "b", Run: "dbt test"}}},
		"handmade": {Slug: "handmade", Nodes: []wf.Node{{ID: "a", Run: "/opt/brevis/bin/opaque"}}},
		"fine":     {Slug: "fine", Nodes: []wf.Node{{ID: "a", Run: "node send.js"}}},
	}}
	return api.NewUI(reads, defs, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler)).
		WithFailing(failingSlugs{"broken"})
}

// rowsOf is the slugs the list draws, in order.
func rowsOf(body string) []string {
	var out []string
	for _, s := range []string{"broken", "busy", "resting", "handmade", "fine"} {
		if strings.Contains(body, `href="/workflows/`+s+`"`) {
			out = append(out, s)
		}
	}
	return out
}

// THE STATE IS ONE SEGMENT OF FIVE QUESTIONS, each with how many it would
// show: all, failing (the last finished run failed -- the sidebar's badge),
// running (running, queued or retrying), paused, manual only.
func TestTheStateSegmentAsksFiveQuestions(t *testing.T) {
	_, body := get(t, fiveUI(), "/workflows")
	for _, s := range []struct{ label, count string }{
		{"All", "5"}, {"Failing", "1"}, {"Running", "2"}, {"Paused", "1"}, {"Manual only", "1"},
	} {
		if !segment(s.label, s.count).MatchString(body) {
			t.Errorf("the segment does not say %s %s", s.label, s.count)
		}
	}
	for q, want := range map[string]string{
		"failing": "broken", "running": "broken busy", "paused": "resting", "manual": "handmade",
	} {
		_, body := answerGet(t, fiveUI(), "/workflows?state="+q)
		if got := strings.Join(rowsOf(body), " "); got != want {
			t.Errorf("state=%s draws %q, want %q", q, got, want)
		}
	}
}

// AN OLD LINK STILL FILTERS, and says how: a state the segment does not offer
// and the schedule filter that went away are chips, each with its ×.
func TestAnOldWorkflowsLinkStillFilters(t *testing.T) {
	_, body := answerGet(t, fiveUI(), "/workflows?state=success&active=paused")
	if got := strings.Join(rowsOf(body), " "); got != "resting" {
		t.Errorf("the old link draws %q, want resting", got)
	}
	for _, remove := range []string{`href="/workflows?active=paused"`, `href="/workflows?state=success"`} {
		if !strings.Contains(body, remove) {
			t.Errorf("no chip removes its own filter with %s", remove)
		}
	}
}

// THE SEARCH FINDS A WORKFLOW BY WHAT IT RUNS and by its tags, not only by
// its name: "dbt" finds the two that run it.
func TestTheWorkflowsSearchReadsCommandsAndTags(t *testing.T) {
	for q, want := range map[string]string{"dbt": "busy resting", "finance": "broken", "fetch.py": "broken"} {
		_, body := answerGet(t, fiveUI(), "/workflows?q="+q)
		if got := strings.Join(rowsOf(body), " "); got != want {
			t.Errorf("q=%s draws %q, want %q", q, got, want)
		}
	}
}

// THE LANGUAGES ARE THE ONES THE WORKFLOWS RUN, each with its logo and how
// many workflows run it; choosing one keeps the workflows that run it.
func TestTheLanguageFilterIsTheLanguagesThatRun(t *testing.T) {
	_, body := get(t, fiveUI(), "/workflows")
	at := strings.Index(body, `data-lang-filter`)
	if at < 0 {
		t.Fatal("there is no language filter")
	}
	seg := body[at : at+strings.Index(body[at:], "</div>")]
	for _, want := range []string{`aria-label="Python"`, `aria-label="dbt"`, `aria-label="Node.js"`, `href="/workflows?lang=dbt"`} {
		if !strings.Contains(seg, want) {
			t.Errorf("the language filter has no %s", want)
		}
	}
	_, body = answerGet(t, fiveUI(), "/workflows?lang=dbt")
	if got := strings.Join(rowsOf(body), " "); got != "busy resting" {
		t.Errorf("lang=dbt draws %q", got)
	}
}
