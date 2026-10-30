package pages

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// THE WORKFLOWS LIST SPEAKS THE READER'S LANGUAGE (screen-workflows T1):
// drawn in Portuguese with everything it can show at once -- every state,
// an older link's chips, a project, tags, languages, a search, pages -- none
// of its English words is left. Workflow, cron and catchup are terms, and
// slugs and tags are the workflows' own.
func TestTheWorkflowsListHasNoEnglishLeft(t *testing.T) {
	next, last := time.Now().Add(58*time.Minute), time.Now().Add(-time.Hour)
	id := "11111111-0000-0000-0000-000000000001"
	rows := []postgres.WorkflowSummary{
		{Slug: "daily", Project: "p", Cron: "0 4 * * *", Catchup: true, HasSchedule: true, Active: true, NextRun: &next,
			LastRunID: &id, LastRunAt: &last, TotalRuns: 3, Tags: []string{"finance"}},
		{Slug: "resting", Project: "p", Cron: "0 5 * * *", HasSchedule: true},
		{Slug: "handmade", Project: "p"},
	}
	v := WorkflowsScreen{
		Rows: rows, Tags: []string{"finance"}, Langs: []LangChoice{{Key: "python", Count: 2}, {Key: "dbt", Count: 1}},
		Filter: Filter{Search: "da", State: "success", Active: "paused", Project: "p", Tags: []string{"finance"},
			Lang: "dbt", Page: 1, PerPage: 2},
		Counts: WorkflowCounts{State: map[string]int{"": 3}},
		Recent: map[string][]postgres.RecentRun{"daily": {{ID: id, Status: "success", At: last}}},
		Total:  9, Filtered: 3,
	}
	text := visibleText(drawIn(t, "pt-BR", Workflows(v))) + " " +
		visibleText(drawIn(t, "pt-BR", WorkflowsList(WorkflowsScreen{Total: 1, Filter: Filter{Page: 1, PerPage: 25}})))
	for _, english := range []string{
		"A workflow is", "Search", "State", "All", "Failing", "Running", "Paused", "Manual only",
		"last run", "schedule", "project", "Remove", "Filter tags", "Apply", "Language",
		"with", "success", "clear filters", "Next run", "Last runs", "manual only", "paused",
		"never ran", "runs", "Pause", "Resume", "Run", "previous", "next", "No workflow",
	} {
		if at := regexp.MustCompile(`\b` + regexp.QuoteMeta(english) + `\b`).FindStringIndex(text); at != nil {
			t.Errorf("the Portuguese list still says %q: …%s…", english, text[max(0, at[0]-60):min(len(text), at[1]+40)])
		}
	}
	if !strings.Contains(text, "Só manual") || !strings.Contains(text, "de 9 workflows") {
		t.Error("the Portuguese list is not the catalog's")
	}
}
