package pages

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// THE RUNS SCREEN SPEAKS THE READER'S LANGUAGE, ALL OF IT (screen-runs T8):
// drawn in Portuguese with everything it can show at once -- a list, a failed
// run with its cause, a queue line, alerts, parameters -- none of its English
// words is left. Product terms (slot, workflow, Logs) are not words to
// translate, and the run's own text (its log, its step names) is the run's.
func TestTheRunsScreenHasNoEnglishLeft(t *testing.T) {
	p := multiStep(t)
	p.Run.Params = map[string]string{"load_full": "true"}
	slot := time.Date(2026, 10, 26, 22, 18, 0, 0, time.UTC)
	p.Run.LogicalDate = &slot
	p.Workflow = &wf.Workflow{Slug: "environment_alert"}
	p.Streak = 2
	p.Raised = []alerts.Record{{Item: alerts.Item{Kind: alerts.KindRun, Channel: alerts.ChannelSlack, Attempts: 2}, CreatedAt: created}}
	queued := RunPage{Run: run.Run{ID: uuid.New(), WorkflowSlug: "w", Status: run.StatusQueued, CreatedAt: created},
		Wait: &postgres.Wait{Group: "warehouse", Limit: 2, Holders: []postgres.Holder{{RunID: uuid.New(), Workflow: "x", Since: created}}, Ahead: 3}}

	screen := Runs(RunsScreen{
		Runs:   []postgres.RunSummary{{ID: p.Run.ID.String(), WorkflowSlug: "environment_alert", Status: "failed", CreatedAt: created}},
		Filter: RunFilter{State: "canceled", Page: 1, PerPage: 25}, Total: 1, Detail: &p,
	})
	text := visibleText(drawIn(t, "pt-BR", screen)) + " " + visibleText(drawIn(t, "pt-BR", RunPane(queued)))
	for _, english := range []string{
		"Runs", "Live", "Triggered", "Scheduled", "Started", "Running", "Finished", "succeeded",
		"Waiting for", "held by", "ahead of it", "Step output", "Alerts", "run failed", "tries",
		"Rerun", "following this run", "details", "Parameters", "Published context", "Search the log",
		"Wrap lines", "Copy", "Go to the error", "View workflow", "Trigger", "Duration", "in a row",
		"No run", "Remove this filter", "attempt", "State", "Command", "Exit", "Steps",
	} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(english) + `\b`).MatchString(text) {
			at := regexp.MustCompile(`\b` + regexp.QuoteMeta(english) + `\b`).FindStringIndex(text)
			t.Errorf("the Portuguese screen still says %q: …%s…", english, text[max(0, at[0]-80):min(len(text), at[1]+40)])
		}
	}
	for _, w := range stateWords {
		if strings.Contains(text, w) {
			t.Errorf("the Portuguese screen still writes the state %q", w)
		}
	}
}

// visibleText is what a reader sees or hears: the text between tags, and the
// labels and titles the tags carry. The rest -- class names, URLs, scripts,
// the CSS -- is not for reading.
// stateWords are the states as the screen writes them beside other text --
// a log's heading, the timeline's end -- where the run's own log may also
// say "failed" in its words.
var stateWords = []string{"· failed", "· success", "failed ·", "succeeded"}

func visibleText(page string) string {
	page = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>`).ReplaceAllString(page, " ")
	var out []string
	for _, m := range regexp.MustCompile(`(?:aria-label|title|placeholder)="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	out = append(out, regexp.MustCompile(`<[^>]*>`).ReplaceAllString(page, " "))
	return strings.Join(out, " ")
}
