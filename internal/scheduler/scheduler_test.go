package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

func inUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// build prepares the project, a published workflow and the scheduler.
func build(t *testing.T, cron string, catchup bool) (*scheduler.Scheduler, *postgres.RunRepo, *queue.Queue, *postgres.Pool) {
	t.Helper()
	pool := testDB(t)
	ctx := context.Background()

	projeto := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, slug, name) VALUES ($1, 'p', 'Projeto')`, projeto); err != nil {
		t.Fatal(err)
	}

	wRepo := postgres.NewWorkflowRepo(pool)
	w := wf.Workflow{
		Slug: "diario", Name: "diario", Kind: wf.KindChain, Schedule: cron,
		Nodes: []wf.Node{{ID: "a", Run: "echo ok"}},
	}
	if err := wRepo.Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}
	if catchup {
		if _, err := pool.Exec(ctx,
			`UPDATE schedules SET catchup = true WHERE workflow_slug = 'diario'`); err != nil {
			t.Fatal(err)
		}
	}

	runs := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)
	s := scheduler.NewScheduler(postgres.NewScheduleRepo(pool), wRepo, runs, fila,
		noLog(), scheduler.OpcoesScheduler{})
	return s, runs, fila, pool
}

func setLastSlot(t *testing.T, pool *postgres.Pool, quando time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE schedules SET ultimo_slot = $1 WHERE workflow_slug = 'diario'`, quando); err != nil {
		t.Fatal(err)
	}
}

// Publishing writes the graph and the schedule together, and the scheduler
// materializes the slot.
func TestTheSchedulerCreatesARunAndEnqueuesIt(t *testing.T) {
	s, runs, fila, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))

	n, err := s.Ciclo(ctx, inUTC("2026-01-02T03:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("created %d runs, wanted 1", n)
	}

	contagem, _ := runs.ContarPorStatus(ctx)
	if contagem[dom.StatusQueued] != 1 {
		t.Errorf("queued = %d, wanted 1", contagem[dom.StatusQueued])
	}
	porTrigger, _ := runs.ContarPorTrigger(ctx)
	if porTrigger["schedule"] != 1 {
		t.Errorf("trigger_type = %v, wanted schedule", porTrigger)
	}
	pendentes, _, _ := fila.Tamanho(ctx)
	if pendentes != 1 {
		t.Errorf("the queue holds %d, wanted 1 -- the scheduler creates AND enqueues", pendentes)
	}
}

// The point of the idempotency: re-running the cycle at the same instant must
// not
// duplicar. E o caso da secao 29 — o scheduler cai e sobe de novo.
func TestARepeatedCycleDoesNotDuplicate(t *testing.T) {
	s, runs, _, pool := build(t, "0 2 * * *", true)
	ctx := context.Background()
	setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))
	agora := inUTC("2026-01-04T03:00:00Z")

	primeiro, err := s.Ciclo(ctx, agora)
	if err != nil {
		t.Fatal(err)
	}
	segundo, err := s.Ciclo(ctx, agora)
	if err != nil {
		t.Fatal(err)
	}

	if primeiro != 3 { // 02, 03, 04
		t.Errorf("first cycle created %d, wanted 3", primeiro)
	}
	if segundo != 0 {
		t.Errorf("second cycle created %d, wanted 0", segundo)
	}
	c, _ := runs.ContarPorStatus(ctx)
	if total := c[dom.StatusQueued]; total != 3 {
		t.Errorf("total runs = %d, wanted 3", total)
	}
}

// catchup=false materializes only the most recent slot, even with days of gap.
func TestCatchupFalseDoesNotRedoThePast(t *testing.T) {
	s, runs, _, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))

	n, err := s.Ciclo(ctx, inUTC("2026-01-10T03:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("created %d runs, wanted 1 — catchup=false ignores the gap", n)
	}
	c, _ := runs.ContarPorStatus(ctx)
	if c[dom.StatusQueued] != 1 {
		t.Errorf("queued = %d, wanted 1", c[dom.StatusQueued])
	}
}

// A backfill enters the queue like any run, with its own trigger and a lower
// priority -- section 12 requires it to respect concurrency and priority.
func TestABackfillEntersTheQueueWithLowerPriority(t *testing.T) {
	s, runs, fila, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	setLastSlot(t, pool, inUTC("2026-03-01T02:00:00Z"))

	n, err := s.Backfill(ctx, "diario", inUTC("2026-01-01T00:00:00Z"), inUTC("2026-01-05T23:59:00Z"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("the backfill created %d runs, wanted 5", n)
	}

	porTrigger, _ := runs.ContarPorTrigger(ctx)
	if porTrigger["backfill"] != 5 {
		t.Errorf("trigger = %v, wanted 5 backfill", porTrigger)
	}
	pendentes, _, _ := fila.Tamanho(ctx)
	if pendentes != 5 {
		t.Errorf("the queue holds %d, wanted 5", pendentes)
	}

	var prio int
	if err := pool.QueryRow(ctx, `SELECT min(prioridade) FROM queue_items`).Scan(&prio); err != nil {
		t.Fatal(err)
	}
	if prio >= 0 {
		t.Errorf("priority = %d; a backfill has to yield to the current work", prio)
	}
}

// A backfill fills the past and must NOT advance the marker, or the scheduler
// would skip future slots that have not happened yet.
func TestABackfillDoesNotAdvanceTheMarker(t *testing.T) {
	s, _, _, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	marcador := inUTC("2026-03-01T02:00:00Z")
	setLastSlot(t, pool, marcador)

	if _, err := s.Backfill(ctx, "diario", inUTC("2026-01-01T00:00:00Z"), inUTC("2026-01-03T23:59:00Z"), nil); err != nil {
		t.Fatal(err)
	}

	var depois time.Time
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot FROM schedules WHERE workflow_slug = 'diario'`).Scan(&depois); err != nil {
		t.Fatal(err)
	}
	if !depois.Equal(marcador) {
		t.Errorf("ultimo_slot = %v, should still be %v", depois, marcador)
	}
}

// Republishing a workflow must not make the scheduler recreate slots already
// materializados.
func TestRepublishingPreservesTheMarker(t *testing.T) {
	_, _, _, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	marcador := inUTC("2026-05-01T02:00:00Z")
	setLastSlot(t, pool, marcador)

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects LIMIT 1`).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{
		Slug: "diario", Name: "diario v2", Kind: wf.KindChain, Schedule: "0 3 * * *",
		Nodes: []wf.Node{{ID: "a", Run: "echo v2"}},
	}
	if err := postgres.NewWorkflowRepo(pool).Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}

	var depois time.Time
	var cron string
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot, cron FROM schedules WHERE workflow_slug = 'diario'`).Scan(&depois, &cron); err != nil {
		t.Fatal(err)
	}
	if !depois.Equal(marcador) {
		t.Errorf("ultimo_slot = %v; republishing must not recreate the past", depois)
	}
	if cron != "0 3 * * *" {
		t.Errorf("cron = %q; the new one should win", cron)
	}
}

// Taking `schedule` out of the YAML has to unschedule, and not leave the old
// schedule alive.
func TestPublishingWithNoCronRemovesTheSchedule(t *testing.T) {
	_, _, _, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects LIMIT 1`).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "diario", Name: "diario", Kind: wf.KindChain,
		Nodes: []wf.Node{{ID: "a", Run: "echo"}}}
	if err := postgres.NewWorkflowRepo(pool).Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schedules WHERE workflow_slug = 'diario'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the schedule should have been removed")
	}
}

// A one-day backfill with an hourly cron has to give 24 slots, not 23: `Next(t)`
// returns the next one strictly after `t`, so starting exactly at
// `de` excluiria o slot da meia-noite.
func TestABackfillIncludesTheEdgeSlot(t *testing.T) {
	s, _, _, pool := build(t, "0 * * * *", false)
	ctx := context.Background()
	setLastSlot(t, pool, inUTC("2026-06-01T00:00:00Z"))

	n, err := s.Backfill(ctx, "diario",
		inUTC("2026-01-01T00:00:00Z"), inUTC("2026-01-01T23:59:59Z"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 24 {
		t.Errorf("the backfill created %d slots, wanted 24 (00:00 to 23:00)", n)
	}
}

// The folder is the source of truth -- but publishing only ever ADDED. Taking a
// file out of it took nothing out of the database, and the scheduler went on
// materializing runs for a workflow nobody could see any more. With a 15-minute
// cron, that is invisible work running forever.
func TestPruningRemovesWhatLeftTheFolder(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)
	agendas := postgres.NewScheduleRepo(pool)

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, slug, name) VALUES ($1,'acme','acme') RETURNING id`,
		uuid.New()).Scan(&projeto); err != nil {
		t.Fatal(err)
	}

	fica := wf.Workflow{Slug: "id_verification", Name: "id", Schedule: "0 4 * * *",
		Nodes: []wf.Node{{ID: "run", Run: "dbt build"}}}
	sai := wf.Workflow{Slug: "vendors_ana_telemetry", Name: "ana", Schedule: "*/15 * * * *",
		Nodes: []wf.Node{{ID: "run", Run: "echo x"}}}
	for _, w := range []wf.Workflow{fica, sai} {
		if err := repo.Publicar(ctx, w, projeto); err != nil {
			t.Fatal(err)
		}
	}

	// An old run of what is about to go: the history has to survive.
	if _, err := postgres.NewRunRepo(pool).Criar(ctx, dom.Run{
		WorkflowSlug: sai.Slug, IdempotencyKey: "antiga", Definition: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	removidos, err := repo.Podar(ctx, projeto, []string{fica.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if len(removidos) != 1 || removidos[0] != sai.Slug {
		t.Fatalf("removidos = %v, want apenas %s", removidos, sai.Slug)
	}

	if _, err := repo.Definition(ctx, sai.Slug); err == nil {
		t.Error("the removed workflow still has a definition in the database")
	}
	if _, err := repo.Definition(ctx, fica.Slug); err != nil {
		t.Errorf("the workflow that stayed is gone: %v", err)
	}

	// The schedule lives in a separate table, linked by slug as text -- the
	// CASCADE does not reach it, and an orphaned schedule would go on creating
	// runs.
	ativas, err := agendas.Ativas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ativas {
		if a.WorkflowSlug == sai.Slug {
			t.Error("the removed workflow's schedule survived and would keep creating runs")
		}
	}

	var historico int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runs WHERE workflow_slug = $1`, sai.Slug).Scan(&historico); err != nil {
		t.Fatal(err)
	}
	if historico != 1 {
		t.Errorf("the history was deleted along with it (%d runs); deleting the run would be deleting the evidence", historico)
	}
}

// With nothing to prune, it touches nothing.
func TestPruningWithNoDifferenceRemovesNothing(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, slug, name) VALUES ($1,'acme','acme') RETURNING id`,
		uuid.New()).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "so_esse", Name: "x", Nodes: []wf.Node{{ID: "a", Run: "echo"}}}
	if err := repo.Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}

	removidos, err := repo.Podar(ctx, projeto, []string{"so_esse"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removidos) != 0 {
		t.Errorf("it removed %v for no reason", removidos)
	}
}

// A newly published schedule (`ultimo_slot` NULL) has to start firing.
//
// This is the test that was missing, and the gap had a shape: EVERY case above
// calls `setLastSlot` before the cycle, so the null-marker path was never
// exercised. In dev, 18 workflows sat registered for hours with not one
// automatic run -- a `*/30` among them -- because `Slots` started from
// proprio `agora`, o proximo horario do cron era sempre futuro, e o marcador
// never left NULL to break the circle.
func TestANewScheduleStartsFiring(t *testing.T) {
	s, runs, _, _ := build(t, "*/30 * * * *", false)
	ctx := context.Background()

	// First cycle: it only plants the marker, without running the past.
	n, err := s.Ciclo(ctx, inUTC("2026-01-01T10:05:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the first cycle created %d runs; the slot before registration is not ours", n)
	}

	// The second cycle, after the clock passed 10:30: now it fires.
	n, err = s.Ciclo(ctx, inUTC("2026-01-01T10:31:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("created %d runs after the cron's time; wanted 1", n)
	}
	porTrigger, _ := runs.ContarPorTrigger(ctx)
	if porTrigger["schedule"] != 1 {
		t.Errorf("trigger = %v; wanted schedule", porTrigger)
	}
}

// The debut marker must not be replanted on every cycle: if it were, `de` would
// advance along with the clock and the schedule would go back to never firing.
func TestTheFirstRunMarkerIsPlantedOnlyOnce(t *testing.T) {
	s, _, _, pool := build(t, "*/30 * * * *", false)
	ctx := context.Background()

	if _, err := s.Ciclo(ctx, inUTC("2026-01-01T10:05:00Z")); err != nil {
		t.Fatal(err)
	}
	var primeiro time.Time
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot FROM schedules WHERE workflow_slug = 'diario'`).Scan(&primeiro); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Ciclo(ctx, inUTC("2026-01-01T10:10:00Z")); err != nil {
		t.Fatal(err)
	}
	var depois time.Time
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot FROM schedules WHERE workflow_slug = 'diario'`).Scan(&depois); err != nil {
		t.Fatal(err)
	}
	if !depois.Equal(primeiro) {
		t.Errorf("the marker moved from %s to %s -- the schedule would never catch up to a time", primeiro, depois)
	}
}
