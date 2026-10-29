package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/run"
)

func head(t *testing.T, r run.Run) string {
	t.Helper()
	var b strings.Builder
	if err := runHead(RunPage{Run: r}).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A RUN THAT CAN STILL BE STOPPED HAS THE BUTTON, and asks before it does:
// stopping cannot be undone. It is in the head the page refreshes, so it goes
// away the moment the run ends.
func TestARunInFlightCanBeCanceled(t *testing.T) {
	for _, s := range []run.Status{run.StatusCreated, run.StatusQueued, run.StatusRunning, run.StatusRetrying} {
		r := run.Run{ID: uuid.New(), WorkflowSlug: "daily", Status: s}
		got := head(t, r)
		if !strings.Contains(got, `action="/runs/`+r.ID.String()+`/cancel"`) || !strings.Contains(got, "data-confirm=") {
			t.Errorf("%s: no cancel form that asks first:\n%s", s, got)
		}
	}
}

// Once asked, the run says it is stopping instead of offering it again.
func TestARunAskedToStopSaysSo(t *testing.T) {
	asked := time.Now()
	got := head(t, run.Run{ID: uuid.New(), Status: run.StatusRunning, CancelRequestedAt: &asked})
	if strings.Contains(got, "/cancel") || !strings.Contains(got, "Canceling…") {
		t.Errorf("a run asked to stop:\n%s", got)
	}
}

// What has ended has nothing to cancel.
func TestAnEndedRunHasNoCancel(t *testing.T) {
	for _, s := range []run.Status{run.StatusSuccess, run.StatusFailed, run.StatusCanceled} {
		if got := head(t, run.Run{ID: uuid.New(), Status: s}); strings.Contains(got, "/cancel") || strings.Contains(got, "Canceling") {
			t.Errorf("%s offers a cancel:\n%s", s, got)
		}
	}
}
