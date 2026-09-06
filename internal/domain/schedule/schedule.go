// Package schedule decides WHEN a workflow should run.
//
// Section 37 of the plan separates the responsibilities without ambiguity: the
// scheduler CREATES runs, the queue EXECUTES them. This package knows nothing of
// the queue, the executor or the database — it answers a pure question: given
// the cron, the timezone, the last slot
// materializado e o instante atual, quais slots faltam?
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

// Schedule e a agenda de um workflow.
type Schedule struct {
	WorkflowSlug string
	Cron         string
	Timezone     string

	// Catchup=false materializa apenas o slot mais recente perdido. Ver Slots.
	Catchup bool

	Ativo bool

	// UltimoSlot is the last slot already materialized. Nil = it never ran.
	UltimoSlot *time.Time
}

// Parse valida o cron e o fuso, devolvendo o agendador pronto.
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
// A politica de catchup e a decisao central desta fase:
//
//   - catchup=true  → EVERY missed slot becomes a run. It serves a pipeline
//     where each day has a meaning of its own and a gap has to be filled.
//   - catchup=false → only the most recent slot. It serves the case where only
//     the
//     estado atual importa, e reprocessar trinta dias seria desperdicio.
//
// `limite` caps the count: a workflow stopped for months with catchup=true would
// create thousands of runs at once and drown the queue. Returning the excess as
// `truncado` makes that visible instead of silent.
func (s Schedule) Slots(agora time.Time, limite int) (slots []time.Time, truncado bool, err error) {
	if !s.Ativo {
		return nil, false, nil
	}
	sched, loc, err := s.Parse()
	if err != nil {
		return nil, false, err
	}

	// The starting point: the last materialized slot, or the current instant
	// when
	// a agenda nunca rodou. Comecar do zero criaria a historia inteira do cron.
	de := agora.In(loc)
	if s.UltimoSlot != nil {
		de = s.UltimoSlot.In(loc)
	}

	// Without catchup, only the MOST RECENT slot matters — the gap is discarded
	// by definition. It walks without accumulating, and the cap does not apply:
	// there is nothing to truncate when only one slot will be materialized.
	//
	// The iteration ceiling protects against a schedule with a very old
	// ultimo_slot, which would make the loop walk years of cron every cycle.
	if !s.Catchup {
		const maxIter = 500_000
		var ultimo time.Time
		for i := 0; i < maxIter; i++ {
			prox := sched.Next(de)
			if prox.After(agora) {
				break
			}
			ultimo, de = prox, prox
		}
		if ultimo.IsZero() {
			return nil, false, nil
		}
		return []time.Time{ultimo}, false, nil
	}

	for {
		prox := sched.Next(de)
		if prox.After(agora) {
			break
		}
		slots = append(slots, prox)
		de = prox

		// Trunca e SINALIZA. O restante entra nos ciclos seguintes, porque o
		// The marker advances on every materialized slot.
		if limite > 0 && len(slots) >= limite {
			return slots, true, nil
		}
	}
	return slots, false, nil
}

// Proximo returns the next trigger after `agora`, for display.
func (s Schedule) Proximo(agora time.Time) (time.Time, error) {
	sched, loc, err := s.Parse()
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(agora.In(loc)), nil
}
