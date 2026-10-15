package pages

import (
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/web/components"
)

// Phase is one step of a run's life on the run page's strip: how it was made,
// when it started, how long it ran, how it ended. State is "done", "current",
// "pending" or "failed".
type Phase struct {
	Key, Label, Detail, State string
}

// PhasesOf reads a run's phases from its own times. The STATUS says which
// phase is now -- a queued run is waiting to start, a running or retrying one
// is running, a finished one is over -- and the times fill in the details:
// the slot, how late it started, the time running (as of now, so the live
// region's refresh moves it), and how it ended.
func PhasesOf(r run.Run, now time.Time) []Phase {
	made := Phase{Key: "scheduled", Label: "Triggered", State: "done",
		Detail: components.UTCStamp(&r.CreatedAt)}
	if r.LogicalDate != nil {
		made.Label, made.Detail = "Scheduled", "slot "+components.UTCStamp(r.LogicalDate)
	}
	started := Phase{Key: "started", Label: "Started", State: "pending"}
	running := Phase{Key: "running", Label: "Running", State: "pending"}
	finished := Phase{Key: "finished", Label: "Finished", State: "pending"}

	// How late it started: after its SLOT for a scheduled run -- the same
	// number as its `delay` auto param, so the page does not show two delays
	// -- and after the trigger for one made by hand.
	from := r.CreatedAt
	if r.LogicalDate != nil {
		from = *r.LogicalDate
	}
	if r.StartedAt != nil {
		wait := r.StartedAt.Sub(from)
		started.Detail = "+" + components.Duration(&wait)
	}

	switch r.Status {
	case run.StatusCreated, run.StatusQueued, run.StatusPending:
		started.State = "current"
		waited := now.Sub(from)
		started.Detail = "waiting " + components.Duration(&waited)
	case run.StatusRunning, run.StatusRetrying:
		started.State, running.State = "done", "current"
		if r.StartedAt != nil {
			ran := now.Sub(*r.StartedAt)
			running.Detail = components.Duration(&ran)
		}
	case run.StatusSuccess, run.StatusFailed, run.StatusCanceled:
		started.State, running.State = "done", "done"
		if r.StartedAt != nil && r.FinishedAt != nil {
			ran := r.FinishedAt.Sub(*r.StartedAt)
			running.Detail = components.Duration(&ran)
		}
		finished.State, finished.Detail = "done", "succeeded"
		if r.Status != run.StatusSuccess {
			finished.State, finished.Detail = "failed", string(r.Status)
		}
		if r.FinishedAt != nil {
			finished.Detail += " · " + components.UTCStamp(r.FinishedAt)
		}
	}
	return []Phase{made, started, running, finished}
}

// phaseDot is a phase's marker colour.
func phaseDot(state string) string {
	switch state {
	case "done":
		return "bg-state-success"
	case "current":
		return "bg-state-running animate-pulse"
	case "failed":
		return "bg-state-failed"
	}
	return "bg-line-strong"
}
