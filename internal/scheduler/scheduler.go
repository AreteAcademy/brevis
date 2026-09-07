package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	sch "github.com/AreteAcademy/brevis/internal/domain/schedule"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
)

// Scheduler materialises slots into Runs and enqueues them.
//
// It CREATES runs and nothing else. Whoever executes is the Dispatcher, draining
// the queue -- §37 is explicit about not mixing the two responsibilities. In
// practice that means the Scheduler can go down without interrupting a single
// in-flight run, and the Dispatcher can go down without losing a single slot.
type Scheduler struct {
	agendas   *postgres.ScheduleRepo
	workflows *postgres.WorkflowRepo
	runs      *postgres.RunRepo
	queue     *queue.Queue
	log       *slog.Logger

	intervalo   time.Duration
	maxPerCycle int

	// A lower priority than new work: a large backfill must not delay the
	// current operation.
	backfillPriority int
}

// OpcoesScheduler parameterises the loop.
type OpcoesScheduler struct {
	Interval    time.Duration
	MaxPerCycle int
}

func NewScheduler(a *postgres.ScheduleRepo, w *postgres.WorkflowRepo, r *postgres.RunRepo,
	f *queue.Queue, log *slog.Logger, o OpcoesScheduler) *Scheduler {

	if o.Interval <= 0 {
		o.Interval = 10 * time.Second
	}
	if o.MaxPerCycle <= 0 {
		// A ceiling per schedule and per cycle: a workflow idle for months with
		// catchup=true would create thousands of runs at once and drown the
		// queue.
		o.MaxPerCycle = 100
	}
	return &Scheduler{
		agendas: a, workflows: w, runs: r, queue: f, log: log,
		intervalo: o.Interval, maxPerCycle: o.MaxPerCycle, backfillPriority: -10,
	}
}

// Run evaluates the schedules periodically until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	tick := time.NewTicker(s.intervalo)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if n, err := s.Cycle(ctx, time.Now()); err != nil {
				s.log.Error("scheduler cycle", "error", err)
			} else if n > 0 {
				s.log.Info("runs criados", "quantidade", n)
			}
		}
	}
}

// Ciclo evaluates every schedule once. Exported so it can be tested with a fixed
// instant, without waiting on a clock.
func (s *Scheduler) Cycle(ctx context.Context, agora time.Time) (int, error) {
	agendas, err := s.agendas.Ativas(ctx)
	if err != nil {
		return 0, err
	}

	var created int
	for _, a := range agendas {
		n, err := s.materialize(ctx, a, agora)
		if err != nil {
			// One schedule with an invalid cron must not stop the others from running.
			s.log.Error("materializing the schedule", "workflow", a.WorkflowSlug, "error", err)
			continue
		}
		created += n
	}
	return created, nil
}

func (s *Scheduler) materialize(ctx context.Context, a sch.Schedule, agora time.Time) (int, error) {
	// A schedule that has never been materialised needs a marker before
	// anything else.
	//
	// Without one, `Slots` starts from `agora` itself, and cron's next time is
	// always strictly in the future: the loop breaks on the first turn, nothing
	// is materialised, and because nothing is materialised the marker never
	// leaves NULL. The schedule stays stuck in that cycle forever -- which is
	// what left 18 workflows registered in dev without ONE automatic run,
	// including a `*/30`, with every run on the screen coming from a manual
	// trigger.
	//
	// Planting `agora` says what is meant: a schedule starts counting from when
	// it went live, and fires at the first time after that. Without running
	// anything this cycle -- the slot before registration is not ours.
	if a.LastSlot == nil {
		if err := s.agendas.AvancarSlot(ctx, a.WorkflowSlug, agora); err != nil {
			return 0, err
		}
		s.log.Info("schedule started", "workflow", a.WorkflowSlug,
			"cron", a.Cron, "primeiro_slot_apos", agora.Format(time.RFC3339))
		return 0, nil
	}

	slots, truncado, err := a.Slots(agora, s.maxPerCycle)
	if err != nil {
		return 0, err
	}
	if truncado {
		// Visible, not silent: truncating without warning makes it look as
		// though the gap was covered.
		s.log.Warn("slots truncados no ciclo", "workflow", a.WorkflowSlug,
			"limite", s.maxPerCycle, "obs", "o restante entra nos proximos ciclos")
	}
	if len(slots) == 0 {
		return 0, nil
	}

	def, err := s.workflows.Definition(ctx, a.WorkflowSlug)
	if err != nil {
		return 0, err
	}
	bruto, err := json.Marshal(def)
	if err != nil {
		return 0, err
	}

	var created int
	for _, slot := range slots {
		// Scheduling uses the DEFAULTS: there is nobody to supply values at four
		// in the morning, and a cron that required params would never fire.
		defaults, err := def.Resolver(nil)
		if err != nil {
			return created, fmt.Errorf("params padrao de %q: %w", a.WorkflowSlug, err)
		}
		if err := s.createAndEnqueue(ctx, a.WorkflowSlug, bruto, slot,
			sch.TriggerSchedule, 0, defaults, def.MaxActive); err != nil {
			return created, err
		}
		created++

		// The marker advances at EVERY slot, and not at the end of the loop: if
		// the process dies midway, the slots already materialised are not
		// recreated.
		if err := s.agendas.AvancarSlot(ctx, a.WorkflowSlug, slot); err != nil {
			return created, err
		}
	}
	return created, nil
}

// createAndEnqueue creates the Run and puts it in the queue.
//
// The idempotency key is `slug:trigger:slot`. It is what makes the scheduler
// safe across a restart: if it dies after creating the Run and before advancing
// the marker, the next attempt collides on the unique instead of duplicating --
// exactly §29's case.
func (s *Scheduler) createAndEnqueue(ctx context.Context, slug string, def []byte,
	slot time.Time, trigger sch.TriggerType, priority int, params map[string]string,
	maxAtivos int) error {

	key := fmt.Sprintf("%s:%s:%s", slug, trigger, slot.UTC().Format(time.RFC3339))

	r, err := s.runs.Create(ctx, dom.Run{
		WorkflowSlug:   slug,
		IdempotencyKey: key,
		Definition:     def,
		TriggerType:    string(trigger),
		LogicalDate:    &slot,
		Params:         params,
		MaxActive:      maxAtivos,
	})
	if err != nil {
		if errors.Is(err, postgres.ErrJaExiste) {
			return nil // already materialized: nothing to do
		}
		return err
	}

	if err := s.runs.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		return err
	}
	return s.queue.Enqueue(ctx, r.ID, priority, time.Time{})
}

// Disparar creates a manual Run and enqueues it now.
//
// A higher priority than scheduled work: whoever clicked is looking at the
// screen. A manual run has no `logical_date` -- it belongs to no slot (§12) --
// and the idempotency key uses the SECOND of the click, which makes two clicks
// in a row a single run rather than two.
func (s *Scheduler) Disparar(ctx context.Context, slug string, agora time.Time,
	params map[string]string) (uuid.UUID, error) {
	def, err := s.workflows.Definition(ctx, slug)
	if err != nil {
		return uuid.Nil, fmt.Errorf("workflow %q: %w", slug, err)
	}
	bruto, err := json.Marshal(def)
	if err != nil {
		return uuid.Nil, err
	}

	// Resolved against the DEFINITION: an invalid value or a param that does not
	// exist fails here, at the moment of the click, and not inside the pod half
	// an hour later.
	values, err := def.Resolver(params)
	if err != nil {
		return uuid.Nil, err
	}

	key := fmt.Sprintf("%s:%s:%s", slug, sch.TriggerManual, agora.UTC().Truncate(time.Second).Format(time.RFC3339))
	r, err := s.runs.Create(ctx, dom.Run{
		WorkflowSlug:   slug,
		IdempotencyKey: key,
		Definition:     bruto,
		TriggerType:    string(sch.TriggerManual),
		Params:         values,
		MaxActive:      def.MaxActive,
	})
	if err != nil {
		if errors.Is(err, postgres.ErrJaExiste) {
			return uuid.Nil, nil // a repeated click within the same second
		}
		return uuid.Nil, err
	}
	if err := s.runs.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		return uuid.Nil, err
	}
	return r.ID, s.queue.Enqueue(ctx, r.ID, 10, time.Time{})
}

// Backfill materialises the slots of a past interval.
//
// It enters the queue like any other run, honouring concurrency and priority --
// §12 is explicit about that. The negative priority makes a backfill give way to
// current work instead of competing with it.
func (s *Scheduler) Backfill(ctx context.Context, slug string, de, ate time.Time,
	params map[string]string) (int, error) {
	agendas, err := s.agendas.Ativas(ctx)
	if err != nil {
		return 0, err
	}

	var alvo *sch.Schedule
	for i := range agendas {
		if agendas[i].WorkflowSlug == slug {
			alvo = &agendas[i]
			break
		}
	}
	if alvo == nil {
		return 0, fmt.Errorf("workflow %q has no active schedule", slug)
	}

	cronSched, loc, err := alvo.Parse()
	if err != nil {
		return 0, err
	}

	def, err := s.workflows.Definition(ctx, slug)
	if err != nil {
		return 0, err
	}
	bruto, err := json.Marshal(def)
	if err != nil {
		return 0, err
	}

	// The params apply to EVERY slot in the interval. That is backfill's use
	// case: "reprocess the whole of January with load_full=true".
	values, err := def.Resolver(params)
	if err != nil {
		return 0, err
	}

	// An instant BEFORE `de`, so a slot exactly at `de` is included: `Next(t)`
	// returns the next one strictly after `t`, so starting at `de` would exclude
	// the 00:00 slot in a full-day backfill.
	var created int
	for cursor := de.In(loc).Add(-time.Nanosecond); ; {
		prox := cronSched.Next(cursor)
		if prox.After(ate) {
			break
		}
		if err := s.createAndEnqueue(ctx, slug, bruto, prox,
			sch.TriggerBackfill, s.backfillPriority, values, def.MaxActive); err != nil {
			return created, err
		}
		created++
		cursor = prox
	}

	// A backfill does NOT touch ultimo_slot: it fills the past, and advancing
	// the marker would make the scheduler skip future slots that have not
	// happened yet.
	return created, nil
}
