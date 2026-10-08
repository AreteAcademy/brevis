package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

const bronzeOrders = "bigquery://acme-prod/bronze/orders"

// triggered publishes one workflow that subscribes, and returns a scheduler.
func triggered(t *testing.T, tr wf.Trigger) (*scheduler.Scheduler, *postgres.Pool) {
	t.Helper()
	pool := testDB(t)
	ctx := context.Background()

	projeto := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, slug, name) VALUES ($1, 'p', 'Projeto')`, projeto); err != nil {
		t.Fatal(err)
	}
	wRepo := postgres.NewWorkflowRepo(pool)
	if err := wRepo.Publicar(ctx, wf.Workflow{
		Slug: "silver", Name: "silver", Kind: wf.KindChain,
		Nodes: []wf.Node{{ID: "a", Run: "brevis-sql build"}}, Trigger: tr,
	}, projeto); err != nil {
		t.Fatal(err)
	}
	s := scheduler.NewScheduler(postgres.NewScheduleRepo(pool), wRepo,
		postgres.NewRunRepo(pool), queue.New(pool.Pool), noLog(), scheduler.SchedulerOptions{})
	return s, pool
}

// land writes a landing straight in, with a recorded_at the test chooses.
//
// RECORDED AND LOADED ARE SET APART ON PURPOSE: `loaded_at` is the step's
// clock and the window must not be cut from it.
func land(t *testing.T, pool *postgres.Pool, writer, target string, recorded time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO landings (run_id, node_id, map_index, target, workflow_slug,
		                      loaded_at, recorded_at)
		VALUES ($1, 'carga', -1, $2, $3, $4, $5)`,
		uuid.New(), target, writer,
		recorded.Add(-7*time.Hour), // what a skewed step would have said
		recorded); err != nil {
		t.Fatal(err)
	}
}

func runCount(t *testing.T, pool *postgres.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM runs WHERE trigger_type = 'landed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// THE ISSUE'S OWN CRITERION: ten landings in a minute start ONE run.
//
// On a fixed `now` and not on a sleep: the window is what is being measured,
// and a test that waited would be measuring the machine.
func TestTenLandingsInAMinuteStartOneRun(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{OnLanded: []string{bronzeOrders}, Debounce: 5 * time.Minute})
	ctx := context.Background()
	t0 := inUTC("2026-03-11T04:00:00Z")

	// The first cycle PLANTS the cursor and starts nothing: a trigger that
	// fired for the whole history of the table on its first turn is the
	// mistake `schedules.ultimo_slot` already made once.
	if n, err := s.Cycle(ctx, t0); err != nil || n != 0 {
		t.Fatalf("the planting cycle created %d runs (%v)", n, err)
	}

	for i := range 10 {
		land(t, pool, "bronze", bronzeOrders, t0.Add(time.Duration(i)*6*time.Second))
	}
	n, err := s.Cycle(ctx, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ten landings created %d runs, wanted 1", n)
	}
	if got := runCount(t, pool); got != 1 {
		t.Errorf("%d runs in the database", got)
	}

	// And a cycle with nothing new creates nothing -- including over the
	// overlap, which re-reads a minute it has already seen.
	if n, err := s.Cycle(ctx, t0.Add(2*time.Minute)); err != nil || n != 0 {
		t.Errorf("a quiet cycle created %d runs (%v)", n, err)
	}
	if got := runCount(t, pool); got != 1 {
		t.Errorf("the overlap created a second run: %d", got)
	}
}

// A LANDING IN THE NEXT WINDOW IS THE NEXT RUN. Without this the first test
// passes on a trigger that fires once and never again.
func TestTheNextWindowIsANewRun(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{OnLanded: []string{bronzeOrders}, Debounce: 5 * time.Minute})
	t0 := inUTC("2026-03-11T04:00:00Z")
	mustCycle(t, s, t0)

	land(t, pool, "bronze", bronzeOrders, t0.Add(time.Minute))
	mustCycle(t, s, t0.Add(2*time.Minute))
	land(t, pool, "bronze", bronzeOrders, t0.Add(7*time.Minute))
	mustCycle(t, s, t0.Add(8*time.Minute))

	if got := runCount(t, pool); got != 2 {
		t.Errorf("%d runs across two windows, wanted 2", got)
	}
}

// A PATTERN SUBSCRIBES TO A DATASET, which is the only thing auto_table's
// tables can be named by -- and it must not reach a dataset that merely
// starts with the same letters.
func TestAPatternMatchesTheDatasetAndNotItsNeighbour(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{OnLanded: []string{"bigquery://acme-prod/bronze/*"}})
	t0 := inUTC("2026-03-11T04:00:00Z")
	mustCycle(t, s, t0)

	land(t, pool, "gw", "bigquery://acme-prod/bronze_raw/orders", t0.Add(time.Second))
	mustCycle(t, s, t0.Add(time.Minute))
	if got := runCount(t, pool); got != 0 {
		t.Fatalf("bronze_raw started %d runs; the pattern leaked past the slash", got)
	}

	land(t, pool, "gw", "bigquery://acme-prod/bronze/clicks", t0.Add(2*time.Minute))
	mustCycle(t, s, t0.Add(3*time.Minute))
	if got := runCount(t, pool); got != 1 {
		t.Errorf("%d runs, wanted 1", got)
	}
}

// A WORKFLOW IS NOT STARTED BY ITS OWN WRITE, at runtime and not only at
// publish: a document stored by an older engine, or edited in the database,
// never passed the domain's invariants. The loop this prevents does not stop
// on its own.
func TestAWorkflowIsNotStartedByItsOwnLanding(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{OnLanded: []string{bronzeOrders}})
	t0 := inUTC("2026-03-11T04:00:00Z")
	mustCycle(t, s, t0)

	land(t, pool, "silver", bronzeOrders, t0.Add(time.Second)) // silver writes it itself
	mustCycle(t, s, t0.Add(time.Minute))
	if got := runCount(t, pool); got != 0 {
		t.Fatalf("a workflow started itself: %d runs", got)
	}

	// Somebody ELSE landing it still starts it.
	land(t, pool, "bronze", bronzeOrders, t0.Add(2*time.Minute))
	mustCycle(t, s, t0.Add(3*time.Minute))
	if got := runCount(t, pool); got != 1 {
		t.Errorf("%d runs, wanted 1", got)
	}
}

// A WORKFLOW WITH NO TRIGGER IS NOT TOUCHED, and neither is the cursor: a
// system where nothing subscribes plants nothing, so the first workflow to
// subscribe does not inherit a window it never asked for.
func TestNothingSubscribesAndNothingIsPlanted(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{})
	ctx := context.Background()
	t0 := inUTC("2026-03-11T04:00:00Z")

	land(t, pool, "bronze", bronzeOrders, t0)
	if n, err := s.Cycle(ctx, t0.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("created %d runs (%v)", n, err)
	}
	var cursors int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM landing_cursor`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if cursors != 0 {
		t.Errorf("a cursor was planted with no subscriptions")
	}
}

// THE RUN CARRIES THE WINDOW AS ITS LOGICAL DATE, which is what makes the
// idempotency key work and what a backfill of this trigger would read.
func TestTheRunsLogicalDateIsTheWindow(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{OnLanded: []string{bronzeOrders}, Debounce: 5 * time.Minute})
	ctx := context.Background()
	t0 := inUTC("2026-03-11T04:00:00Z")
	mustCycle(t, s, t0)

	land(t, pool, "bronze", bronzeOrders, inUTC("2026-03-11T04:07:30Z"))
	mustCycle(t, s, inUTC("2026-03-11T04:09:00Z"))

	var logical time.Time
	var key string
	if err := pool.QueryRow(ctx,
		`SELECT logical_date, idempotency_key FROM runs WHERE trigger_type = 'landed'`).
		Scan(&logical, &key); err != nil {
		t.Fatal(err)
	}
	want := inUTC("2026-03-11T04:05:00Z")
	if !logical.Equal(want) {
		t.Errorf("logical_date = %s, wanted the window %s", logical.UTC(), want)
	}
	if key != "silver:landed:2026-03-11T04:05:00Z" {
		t.Errorf("idempotency_key = %q", key)
	}
}

func mustCycle(t *testing.T, s *scheduler.Scheduler, now time.Time) {
	t.Helper()
	if _, err := s.Cycle(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

// THE RUN CARRIES EVERY TARGET OF ITS WINDOW, not the first one.
//
// #63: "the run receives the triggering landings as auto params". A run that
// knew about one landing out of three would start the right workflow and tell
// it to rebuild one table of the three that moved -- which is worse than not
// firing, because it looks like it worked.
//
// The duplicate is in the fixture on purpose: ten landings on one table is
// one thing to rebuild, and a list naming it ten times is a list nobody reads.
func TestTheRunCarriesEveryTargetOfItsWindow(t *testing.T) {
	s, pool := triggered(t, wf.Trigger{
		OnLanded: []string{"bigquery://acme-prod/bronze/*"},
		Debounce: 5 * time.Minute,
	})
	t0 := inUTC("2026-03-11T04:00:00Z")
	mustCycle(t, s, t0)

	land(t, pool, "bronze", "bigquery://acme-prod/bronze/orders", t0.Add(10*time.Second))
	land(t, pool, "bronze", "bigquery://acme-prod/bronze/items", t0.Add(20*time.Second))
	land(t, pool, "bronze", "bigquery://acme-prod/bronze/orders", t0.Add(30*time.Second))
	mustCycle(t, s, t0.Add(time.Minute))

	var targets []string
	if err := pool.QueryRow(context.Background(),
		`SELECT trigger_targets FROM runs WHERE trigger_type = 'landed'`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("trigger_targets = %v, wanted the two distinct tables", targets)
	}
	seen := map[string]bool{targets[0]: true, targets[1]: true}
	for _, want := range []string{
		"bigquery://acme-prod/bronze/orders", "bigquery://acme-prod/bronze/items"} {
		if !seen[want] {
			t.Errorf("%s is missing from %v", want, targets)
		}
	}
}

// AND A SCHEDULED RUN STORES NULL, not an empty array. Absent and empty are
// the same thing here, and a column that said `{}` would make "no landing
// started this" indistinguishable from "every landing was refused".
func TestAScheduledRunStoresNoTargets(t *testing.T) {
	s, _, _, pool := build(t, "0 2 * * *", false)
	setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))
	mustCycle(t, s, inUTC("2026-01-02T03:00:00Z"))

	var targets []string
	if err := pool.QueryRow(context.Background(),
		`SELECT trigger_targets FROM runs WHERE trigger_type = 'schedule'`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != nil {
		t.Errorf("trigger_targets = %v on a scheduled run", targets)
	}
}
