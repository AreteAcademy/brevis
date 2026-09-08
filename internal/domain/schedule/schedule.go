// Package schedule decides WHEN a workflow should run.
//
// Section 37 of the plan separates the responsibilities without ambiguity: the
// scheduler CREATES runs, the queue EXECUTES them. This package knows nothing of
// the queue, the executor or the database — it answers a pure question: given
// the cron, the timezone, the last materialized slot and the current instant,
// which slots are missing?
//
// Isolating that is what makes the catchup policy testable with no fake clock
// and no Postgres.
package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// TriggerType says why a run came into being (section 12).
type TriggerType string

const (
	TriggerSchedule TriggerType = "schedule"
	TriggerManual   TriggerType = "manual"
	TriggerBackfill TriggerType = "backfill"
	TriggerAPI      TriggerType = "api"
	TriggerRetry    TriggerType = "retry"
)

// Schedule is a workflow's schedule.
type Schedule struct {
	WorkflowSlug string
	Cron         string
	Timezone     string

	// Catchup=false materializes only the most recent missed slot. See Slots.
	Catchup bool

	Active bool

	// LastSlot is the last slot already materialized. Nil = it never ran.
	LastSlot *time.Time
}

// Parse validates the cron and the timezone, returning a ready scheduler.
//
// It validates both TOGETHER because a valid cron in an invalid timezone
// schedules nothing, and the error would only surface in the scheduler's loop,
// far from whoever wrote the file.
func (s Schedule) Parse() (cron.Schedule, *time.Location, error) {
	tz := s.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, nil, fmt.Errorf("timezone %q is not valid: %w", tz, err)
	}

	// No seconds: "0 2 * * *" is a 5-field cron, as in the plan's YAML.
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := p.Parse(s.Cron)
	if err != nil {
		return nil, nil, fmt.Errorf("cron %q is not valid: %w", s.Cron, err)
	}
	return sched, loc, nil
}

// Slots returns the instants that still have to become a Run, up to `agora`.
//
// The catchup policy is this phase's central decision:
//
//   - catchup=true  → EVERY missed slot becomes a run. It serves a pipeline
//     where each day has a meaning of its own and a gap has to be filled.
//   - catchup=false → only the most recent slot. It serves the case where only
//     the current state matters, and reprocessing thirty days would be waste.
//
// `limite` caps the count: a workflow stopped for months with catchup=true would
// create thousands of runs at once and drown the queue. Returning the excess as
// `truncado` makes that visible instead of silent.
func (s Schedule) Slots(now time.Time, limite int) (slots []time.Time, truncado bool, err error) {
	if !s.Active {
		return nil, false, nil
	}
	sched, loc, err := s.Parse()
	if err != nil {
		return nil, false, err
	}

	// The starting point: the last materialized slot, or the current instant
	// when
	// the schedule never ran. Starting from zero would create the cron's entire
	// history.
	de := now.In(loc)
	if s.LastSlot != nil {
		de = s.LastSlot.In(loc)
	}

	// Without catchup, only the MOST RECENT slot matters — the gap is discarded
	// by definition. It walks without accumulating, and the cap does not apply:
	// there is nothing to truncate when only one slot will be materialized.
	//
	// The iteration ceiling protects against a schedule with a very old
	// ultimo_slot, which would make the loop walk years of cron every cycle.
	if !s.Catchup {
		const maxIter = 500_000
		var last time.Time
		for i := 0; i < maxIter; i++ {
			prox := sched.Next(de)
			if prox.After(now) {
				break
			}
			last, de = prox, prox
		}
		if last.IsZero() {
			return nil, false, nil
		}
		return []time.Time{last}, false, nil
	}

	for {
		prox := sched.Next(de)
		if prox.After(now) {
			break
		}
		slots = append(slots, prox)
		de = prox

		// It truncates and SIGNALS. The rest goes into the following cycles,
		// because the marker advances on every materialized slot.
		if limite > 0 && len(slots) >= limite {
			return slots, true, nil
		}
	}
	return slots, false, nil
}

// Next returns the next trigger after `agora`, for display.
func (s Schedule) Next(now time.Time) (time.Time, error) {
	sched, loc, err := s.Parse()
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(now.In(loc)), nil
}

// Window returns the interval a slot covers: from the PREVIOUS slot up to this
// one, end excluded.
//
// A pipeline that asks its source for [start, end) never overlaps and never
// gaps, however late the run was and however many times it retried. Reading
// `now()` instead is the bug this exists to remove: a run delayed forty minutes
// skips forty minutes of data, nothing fails, and it is found weeks later.
//
// It walks FORWARD from a lookback, because a cron expression can say when the
// next slot is and not when the last one was. The lookback doubles rather than
// starting at a year: a `*/10` schedule finds its answer in the first step,
// and only a yearly cron pays for the long one.
//
// Zero when there is no schedule, and zero when nothing was found inside the
// longest lookback -- a slot with no predecessor is the workflow's first, and
// inventing a window for it would hand a pipeline a year of data on its first
// morning.
func (s Schedule) Window(slot time.Time) (start, end time.Time, err error) {
	sched, loc, err := s.Parse()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	slot = slot.In(loc)

	for _, back := range []time.Duration{
		2 * time.Hour, 48 * time.Hour, 40 * 24 * time.Hour, 400 * 24 * time.Hour,
	} {
		cursor := slot.Add(-back)
		var previous time.Time
		for {
			next := sched.Next(cursor)
			if !next.Before(slot) {
				break
			}
			previous, cursor = next, next
		}
		if !previous.IsZero() {
			return previous, slot, nil
		}
	}
	return time.Time{}, time.Time{}, nil
}
