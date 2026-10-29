package pages

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	evkind "github.com/AreteAcademy/brevis/internal/domain/events"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// THE OVERVIEW SPEAKS THE READER'S LANGUAGE, ALL OF IT (screen-overview T7):
// drawn in Portuguese with everything it can show at once -- what needs you,
// the numbers, the chart, what fires next, the recent runs, the event log --
// none of its English words is left. The run's own text and product terms
// are not words to translate.
func TestTheOverviewHasNoEnglishLeft(t *testing.T) {
	now := time.Now()
	slot := now.Add(-time.Hour)
	run1 := uuid.New()
	d := OverviewData{
		Window: ParseWindow("7d"), Now: now, Pending: 2,
		Ind:      postgres.Indicators{Total: 30, Succeeded: 25, Failed: 5, Running: 1, Retried: 2, MeanDuration: time.Minute},
		Buckets:  []postgres.Bucket{{Start: now.Add(-24 * time.Hour), Succeeded: 3, Failed: 1}, {Start: now, Succeeded: 2, Running: 1}},
		Proximas: []NextRun{{Workflow: "daily", Cron: "0 4 * * *", When: now.Add(time.Hour)}, {Workflow: "often", Cron: "*/5 * * * *", When: now.Add(time.Minute)}},
		Recentes: []postgres.RunSummary{
			{ID: run1.String(), WorkflowSlug: "daily", Status: "failed", TriggerType: "schedule", LogicalDate: &slot, CreatedAt: slot, Err: "boom", Attempt: 1},
			{ID: uuid.NewString(), WorkflowSlug: "often", Status: "success", TriggerType: "manual", CreatedAt: now, Duration: durp(3 * time.Second)},
		},
		Attention: Attention{
			Items: []AttentionItem{{Workflow: "daily", RunID: run1, FailedAt: slot, Streak: 3, Rerunnable: true,
				Cause: &diagnosis.Cause{Step: "load", MapIndex: run.Unmapped, ExitCode: intp(1), Line: "boom"}}},
			More:    2,
			Targets: []TargetItem{{Target: "bq.orders", Status: catalog.Late, Workflow: "orders", Missed: slot}},
		},
		Events: []postgres.Event{
			{At: now, Kind: evkind.RunFailed, Subject: "daily", RunID: &run1},
			{At: now, Kind: evkind.SchedulePaused, Subject: "daily", Actor: "ana"},
		},
	}
	text := visibleText(drawIn(t, "pt-BR", Overview(d)))
	for _, english := range []string{
		"Overview", "Needs your attention", "failed", "times in a row", "View log", "Rerun", "and 2 more",
		"missed its run", "Open in catalog", "Success rate", "last 7 days", "Failures", "still failing",
		"Running now", "queued", "average duration", "Next trigger", "Runs per", "success", "running",
		"Next triggers", "All workflows", "every day", "every 5 minutes", "Recent runs", "Event log",
		"See all runs", "State", "Workflow ", "Trigger", "Duration", "Created", "ago", "Schedule of", "by ana",
		"failure", "attempt", "Window", "schedule",
	} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(english) + `\b`).MatchString(text) {
			at := regexp.MustCompile(`\b` + regexp.QuoteMeta(english) + `\b`).FindStringIndex(text)
			t.Errorf("the Portuguese Overview still says %q: …%s…", english, strings.TrimSpace(text[max(0, at[0]-60):min(len(text), at[1]+30)]))
		}
	}
}

func durp(d time.Duration) *time.Duration { return &d }
func intp(n int) *int                     { return &n }
