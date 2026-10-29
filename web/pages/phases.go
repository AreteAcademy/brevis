package pages

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/web/components"
	"github.com/AreteAcademy/brevis/web/i18n"
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

// phaseBar is a phase's bar colour.
func phaseBar(state string) string {
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

// autoItems are a run's auto params as the grid shows them, in the order and
// with the words the cards had; an absent one is left out, as before.
func autoItems(r run.Run) []components.KVItem {
	a := r.Auto
	items := []components.KVItem{
		{Key: "adjusted_at", Value: components.UTCStamp(&a.AdjustedAt), Note: "the clock to read instead of now()"},
		{Key: "date", Value: a.Date, Note: "the UTC day of adjusted_at — the partition"},
		{Key: "delay", Value: lateness(a.DelaySeconds), Note: "how late this attempt started"},
	}
	add := func(key string, t *time.Time, note string) {
		if t != nil {
			items = append(items, components.KVItem{Key: key, Value: components.UTCStamp(t), Note: note})
		}
	}
	add("scheduled_at", a.ScheduledAt, "the slot this run represents")
	add("started_at", a.StartedAt, "when this attempt began")
	add("interval_start", a.IntervalStart, "the window this run covers, from")
	add("interval_end", a.IntervalEnd, "…up to, excluded")
	items = append(items, components.KVItem{Key: "previous_error", Value: fmt.Sprint(a.PreviousError), Note: "did the run before this one fail"})
	add("previous_success_at", a.PreviousSuccessAt, "how far back to catch up from")
	return items
}

// firstLine is an error's first line, for the line that opens to the rest.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// showsWhy is whether the head says why the run failed: a failed run, or a
// retrying one with the failure that sent it back.
func showsWhy(p RunPage) bool {
	return p.Cause != nil && (p.Run.Status == run.StatusFailed || p.Run.Status == run.StatusRetrying)
}

// whyTitle names what stopped the run: its step, and how the step exited
// when it did; "The run failed" when it died before any step.
func whyTitle(ctx context.Context, c *diagnosis.Cause) string {
	if c.Step == "" {
		return i18n.T(ctx, "run.why.run")
	}
	step := c.Step
	if c.MapIndex != run.Unmapped {
		step += "[" + strconv.Itoa(c.MapIndex) + "]"
	}
	if c.ExitCode != nil {
		return i18n.T(ctx, "run.why.step_exit", "step", step, "code", *c.ExitCode)
	}
	return i18n.T(ctx, "run.why.step", "step", step)
}

// whyExcerpt is the lines before the cause and the cause, or the cause alone.
func whyExcerpt(c *diagnosis.Cause) string {
	if len(c.Excerpt) > 0 {
		return strings.Join(c.Excerpt, "\n")
	}
	return c.Line
}

// runDuration is how long the run took, "in progress" while it runs.
func runDuration(ctx context.Context, r run.Run) string {
	if r.Status.InFlight() {
		return i18n.T(ctx, "run.in_progress")
	}
	if r.StartedAt == nil || r.FinishedAt == nil {
		return "—"
	}
	return i18n.Elapsed(ctx, r.FinishedAt.Sub(*r.StartedAt))
}
