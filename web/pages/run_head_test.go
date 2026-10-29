package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func headOf(t *testing.T, p RunPage) string {
	t.Helper()
	page := draw(t, RunPane(p))
	at := strings.Index(page, `id="run-head"`)
	if at < 0 {
		t.Fatal("no run head")
	}
	return page[at:]
}

func exit(n int) *int { return &n }

func failedWith(c *diagnosis.Cause, streak int) RunPage {
	return RunPage{
		Run:   run.Run{ID: uuid.New(), WorkflowSlug: "environment_alert", Status: run.StatusFailed, CreatedAt: created, Err: "level 1: step \"test\" failed"},
		Cause: c, Streak: streak,
	}
}

// WHY IT FAILED IS THE FIRST THING UNDER THE NAME: the step, how it exited and
// the lines that say why, in the run's own words -- and, when it is not the
// first time, how many times in a row.
func TestAFailedRunSaysWhyItFailed(t *testing.T) {
	c := &diagnosis.Cause{Step: "test", MapIndex: run.Unmapped, ExitCode: exit(1), Line: "Error: 1 test failed",
		Excerpt: []string{"2 of 2 FAIL 14 not_null_alerts_id", "Error: 1 test failed"}, LineNo: 4}
	head := headOf(t, failedWith(c, 3))
	why := head[strings.Index(head, "data-why"):]
	for _, want := range []string{"Step test failed (exit 1)", "2 of 2 FAIL 14 not_null_alerts_id\nError: 1 test failed", "Failed 3 times in a row"} {
		if !strings.Contains(why, want) {
			t.Errorf("why it failed lacks %q", want)
		}
	}
	// It takes the place of the run's error line, which said the same thing
	// in the engine's words.
	if strings.Contains(head, "data-run-error") {
		t.Error("the run's error is still drawn under its cause")
	}
}

func TestAFirstFailureDoesNotCountItself(t *testing.T) {
	c := &diagnosis.Cause{Step: "", Line: "no step started", Source: diagnosis.FromRunError}
	head := headOf(t, failedWith(c, 1))
	if !strings.Contains(head, "The run failed") || strings.Contains(head, "in a row") {
		t.Errorf("a first failure with no step: %s", head[strings.Index(head, "data-why"):])
	}
	if !strings.Contains(head, "no step started") {
		t.Error("a cause with no excerpt shows its line")
	}
}

// Only a failure has a cause to show; a retrying run shows the one that sent
// it back, and a run that passed shows none.
func TestOnlyAFailureSaysWhy(t *testing.T) {
	ok := RunPage{Run: run.Run{ID: uuid.New(), WorkflowSlug: "w", Status: run.StatusSuccess, CreatedAt: created}}
	if strings.Contains(headOf(t, ok), "data-why") {
		t.Error("a run that passed says why it failed")
	}
	retry := failedWith(&diagnosis.Cause{Step: "load", MapIndex: run.Unmapped, Line: "timeout"}, 0)
	retry.Run.Status = run.StatusRetrying
	if !strings.Contains(headOf(t, retry), "Step load failed") {
		t.Error("a retrying run does not say what sent it back")
	}
}

// A canceled run's error is who stopped it, and it stays on the page: there
// is no cause to put in its place.
func TestACanceledRunKeepsItsError(t *testing.T) {
	p := RunPage{Run: run.Run{ID: uuid.New(), WorkflowSlug: "w", Status: run.StatusCanceled, CreatedAt: created, Err: "canceled by ana"}}
	head := headOf(t, p)
	if !strings.Contains(head, "data-run-error") || !strings.Contains(head, "canceled by ana") {
		t.Error("the canceled run lost who canceled it")
	}
}

// THE HEAD'S VERBS FOLLOW THE STATE: a run in flight can be canceled, one
// that ended can be run again; the workflow is always one click away.
func TestTheHeadsActionsFollowTheState(t *testing.T) {
	def := wf.Workflow{Slug: "w"}
	running := RunPage{Run: run.Run{ID: uuid.New(), WorkflowSlug: "w", Status: run.StatusRunning, CreatedAt: created}, Workflow: &def}
	head := headOf(t, running)
	if !strings.Contains(head, `/cancel"`) || strings.Contains(head, "data-rerun") {
		t.Error("a running run is not offered Cancel alone")
	}
	if !strings.Contains(head, `href="/workflows/w"`) {
		t.Error("no way to the workflow")
	}
	done := running
	done.Run.Status = run.StatusSuccess
	head = headOf(t, done)
	if strings.Contains(head, `/cancel"`) || !strings.Contains(head, "data-rerun") {
		t.Error("a finished run is not offered Rerun alone")
	}
}

// The head says how it was started, for which slot, and how long it took --
// "in progress" while it runs -- with the short id one click from the
// clipboard.
func TestTheHeadSaysTriggerSlotAndDuration(t *testing.T) {
	slot := time.Date(2026, 10, 26, 22, 18, 0, 0, time.UTC)
	start, end := slot.Add(4*time.Second), slot.Add(16200*time.Millisecond)
	id := uuid.MustParse("a91f03c2-0000-4000-8000-000000000000")
	p := RunPage{Run: run.Run{ID: id, WorkflowSlug: "w", Status: run.StatusSuccess, TriggerType: "scheduled",
		LogicalDate: &slot, CreatedAt: slot, StartedAt: &start, FinishedAt: &end}}
	head := headOf(t, p)
	for _, want := range []string{"scheduled", "2026-10-26 22:18:00 UTC", "12.2s", "a91f03c2", `data-copy="#run-id"`} {
		if !strings.Contains(head, want) {
			t.Errorf("the head lacks %q", want)
		}
	}
	p.Run.Status, p.Run.FinishedAt = run.StatusRunning, nil
	if !strings.Contains(headOf(t, p), "in progress") {
		t.Error("a running run's duration is not in progress")
	}
}

// THE TIMELINE IS FOUR STEPS WITH A BAR ON TOP, coloured by how each went.
func TestTheTimelineBarsSayHowEachPhaseWent(t *testing.T) {
	head := headOf(t, failedWith(nil, 0))
	for _, want := range []string{
		`data-phase="running" data-phase-state="done"`, `data-phase="finished" data-phase-state="failed"`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("no %s", want)
		}
	}
	if strings.Count(head, "data-phase-bar") != 4 {
		t.Error("each phase does not have its bar")
	}
}

// THE TIMELINE IS TWO BY TWO UNTIL ITS PANE HAS ROOM FOR FOUR: the pane, not
// the window -- beside the list, a wide window still gives the run a narrow
// pane.
func TestTheTimelineFollowsItsPane(t *testing.T) {
	page := draw(t, Runs(RunsScreen{Detail: ptr(failedWith(nil, 0))}))
	if !strings.Contains(page, `id="run-pane" class="@container`) {
		t.Error("the run's pane is not a container")
	}
	ol := between(page, "data-timeline", ">")
	if !strings.Contains(ol, "grid-cols-2") || !strings.Contains(ol, "@2xl:grid-cols-4") {
		t.Errorf("the timeline does not follow its pane: %s", ol)
	}
}
