package catalog

import (
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/schedule"
)

// Status is how a destination stands against its writer's schedule.
type Status string

const (
	OnTime      Status = "on_time"
	Late        Status = "late"
	Stale       Status = "stale"
	Paused      Status = "paused"
	Unscheduled Status = "unscheduled"
)

// minGrace is the least a slot is given before it counts as missed. Below
// this, a load that ends at :03 on an hourly schedule would flicker between
// late and on time every hour.
const minGrace = 10 * time.Minute

// Writer is one workflow step writing a destination, as freshness needs it.
type Writer struct {
	// Schedule is the writer's workflow schedule; nil when it has none.
	Schedule *schedule.Schedule

	// Last is when this writer last landed on the destination.
	Last time.Time

	// Lag is how long after its slot this writer's data usually lands: the p90
	// of loaded_at minus the run's slot over its recent landings. Queue,
	// retries and the step's own duration are all in it, which is the point.
	Lag time.Duration
}

// Verdict is a writer's status and what it rests on.
type Verdict struct {
	Status Status

	// Missed is the first slot that went by without a landing; zero when none
	// has. Grace is how long each slot was given.
	Missed time.Time
	Grace  time.Duration
}

// Freshness reads a writer's lateness from its OWN schedule.
//
// It never asks "is this table old" in absolute terms: an hourly table three
// hours old is stale and a daily one is fine, and only the cron knows which.
// The question is how many slots went by, each with its grace, since the last
// landing:
//
//	none -> on time      one -> late      two or more -> stale
//
// The schedule is read through schedule.Schedule.Next, the same reading the
// scheduler and the workflows list use, so a daylight-saving change moves the
// slot here exactly as it moves the run.
//
// A paused schedule is paused and never stale: the operator chose it. A writer
// with no schedule, or one whose cron cannot be read, gets no verdict --
// guessing an interval is the inference this catalog refuses.
func Freshness(w Writer, now time.Time) Verdict {
	if w.Schedule == nil {
		return Verdict{Status: Unscheduled}
	}
	if !w.Schedule.Active {
		return Verdict{Status: Paused}
	}
	grace := max(minGrace, w.Lag)

	first, err := w.Schedule.Next(w.Last)
	if err != nil {
		return Verdict{Status: Unscheduled}
	}
	if !now.After(first.Add(grace)) {
		return Verdict{Status: OnTime, Grace: grace}
	}
	second, err := w.Schedule.Next(first)
	if err != nil {
		return Verdict{Status: Unscheduled}
	}
	if !now.After(second.Add(grace)) {
		return Verdict{Status: Late, Missed: first, Grace: grace}
	}
	return Verdict{Status: Stale, Missed: first, Grace: grace}
}

// rank orders statuses from healthiest. A destination takes its best writer's
// status: data arriving from any writer is data arriving.
var rank = map[Status]int{OnTime: 0, Late: 1, Stale: 2, Paused: 3, Unscheduled: 4}

// Best returns the healthiest of the statuses, or Unscheduled when there are
// none -- no writer, no verdict.
func Best(statuses ...Status) Status {
	best := Unscheduled
	for _, s := range statuses {
		if rank[s] < rank[best] {
			best = s
		}
	}
	return best
}

// MissedSlots lists the writer's ticks after its last landing that are past
// their grace, oldest first, up to limit. A paused or unscheduled writer has
// none: nothing was expected of it.
//
// limit bounds the work as well as the drawing: a minutely writer stopped for
// a week has ten thousand missed ticks, and the page needs a handful.
func MissedSlots(w Writer, now time.Time, limit int) []time.Time {
	if w.Schedule == nil || !w.Schedule.Active || limit <= 0 {
		return nil
	}
	grace := max(minGrace, w.Lag)
	var out []time.Time
	tick := w.Last
	for len(out) < limit {
		next, err := w.Schedule.Next(tick)
		if err != nil || now.Before(next.Add(grace)) {
			break
		}
		out = append(out, next)
		tick = next
	}
	return out
}
