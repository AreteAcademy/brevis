package pages

import (
	"context"
	"regexp"
	"testing"
	"time"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// THE WORKFLOW PAGE'S HEAD, NUMBERS AND TABS SPEAK THE READER'S LANGUAGE
// (screen-workflows T5). The calendar, the graph and the drawer are their
// own tasks' (T7-T9), and say so in their own tests.
func TestTheWorkflowPagesHeadAndTabsHaveNoEnglishLeft(t *testing.T) {
	now := time.Now()
	next := now.Add(9 * time.Hour)
	slot := now.Add(-24 * time.Hour)
	d := 90 * time.Second
	p := WorkflowPage{
		Def: wf.Workflow{Slug: "daily", Schedule: "0 9 * * *", Tags: []string{"sales"},
			Nodes:  []wf.Node{{ID: "a", Run: "python a.py"}, {ID: "b", Run: "dbt build"}},
			Edges:  []wf.Edge{{From: "a", To: "b"}},
			Params: []wf.Param{{Name: "full", Type: wf.ParamBool, Default: "false", Title: "Carga completa", Group: "Carga"}, {Name: "since", Type: wf.ParamString}}},
		Sum:   &postgres.WorkflowSummary{Slug: "daily", Project: "zarv", Cron: "0 9 * * *", HasSchedule: true, Active: true, NextRun: &next},
		Langs: []string{"python", "dbt"},
		Latest: []postgres.RunSummary{
			{ID: "1", Status: "success", TriggerType: "schedule", LogicalDate: &slot, CreatedAt: slot, Duration: &d},
			{ID: "2", Status: "failed", TriggerType: "manual", LogicalDate: &slot, CreatedAt: slot, Params: map[string]string{"full": "true"}},
		},
		Stats: WorkflowStats{Window: 30 * 24 * time.Hour, Ind: postgres.Indicators{Running: 1, Pending: 1, MeanDuration: d},
			Days: []postgres.Day{{Date: slot, Total: 2, Succeeded: 1, Failed: 1}}},
		Now: now, Day: &slot,
	}
	text := visibleText(drawIn(t, "pt-BR", workflowMeta(p))) + " " + visibleText(drawIn(t, "pt-BR", workflowTabs(p))) + " " +
		visibleText(drawIn(t, "pt-BR", pauseAction(*p.Sum)))
	for _, s := range workflowPageStats(i18n.WithLang(context.Background(), "pt-BR"), p) {
		text += " " + s.Label + " " + s.Value + " " + s.Note
	}
	for _, english := range []string{
		"Active", "Paused", "project", "steps", "dependency", "Days without failure", "Days with failure",
		"Mean duration", "In progress", "Next run", "last 30 days", "of", "days", "running", "queued", "in",
		"Runs", "Accepted parameters", "Parameters", "State", "Trigger", "Duration", "When", "default",
		"All runs", "Show the last runs", "Name", "Type", "Default", "What it is for", "required", "No parameters",
		"schedule", "Pause", "Resume", "day", "Definition", "published workflow", "could not be written",
	} {
		if at := regexp.MustCompile(`\b` + regexp.QuoteMeta(english) + `\b`).FindStringIndex(text); at != nil {
			t.Errorf("the Portuguese page still says %q: …%s…", english, text[max(0, at[0]-60):min(len(text), at[1]+40)])
		}
	}
}
