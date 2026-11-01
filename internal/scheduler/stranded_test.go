package scheduler_test

import (
	"context"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// A process that dies between creating a run and enqueueing it must not lose
// the slot: the next cycle collides on the idempotency key and finishes the job.
// Before, that collision was read as "already materialized" and the run sat in
// `created`/`queued` with no queue item for good.
func TestACrashBeforeTheEnqueueIsRepairedByTheNextCycle(t *testing.T) {
	for _, status := range []string{"created", "queued"} {
		t.Run(status, func(t *testing.T) {
			s, _, _, pool := build(t, "0 2 * * *", false)
			ctx := context.Background()
			setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))
			now := inUTC("2026-01-02T03:00:00Z")

			if _, err := s.Cycle(ctx, now); err != nil {
				t.Fatal(err)
			}
			// The crash: the run exists, the queue item does not.
			if _, err := pool.Exec(ctx, `DELETE FROM queue_items`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE runs SET status = $1`, status); err != nil {
				t.Fatal(err)
			}
			setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))

			n, err := s.Cycle(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("a repaired run is not a created one: counted %d", n)
			}
			assertOneRunOneItem(t, pool, ctx)

			// And a third pass changes nothing.
			setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))
			if _, err := s.Cycle(ctx, now); err != nil {
				t.Fatal(err)
			}
			assertOneRunOneItem(t, pool, ctx)
		})
	}
}

// A run that already has its queue item is left alone: not enqueued twice, and
// not touched.
func TestARunAlreadyEnqueuedIsNotEnqueuedAgain(t *testing.T) {
	s, _, _, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	now := inUTC("2026-01-02T03:00:00Z")
	setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))
	if _, err := s.Cycle(ctx, now); err != nil {
		t.Fatal(err)
	}
	setLastSlot(t, pool, inUTC("2026-01-01T02:00:00Z"))
	if _, err := s.Cycle(ctx, now); err != nil {
		t.Fatal(err)
	}
	assertOneRunOneItem(t, pool, ctx)
}

func TestAManualRunThatCrashedBeforeTheEnqueueIsFinishedByTheClickAgain(t *testing.T) {
	s, _, _, pool := build(t, "0 2 * * *", false)
	ctx := context.Background()
	now := inUTC("2026-01-02T03:00:00Z")

	first, err := s.Disparar(ctx, "diario", now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM queue_items`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runs SET status = 'created'`); err != nil {
		t.Fatal(err)
	}
	again, err := s.Disparar(ctx, "diario", now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("the repair should return the same run: %s vs %s", again, first)
	}
	assertOneRunOneItem(t, pool, ctx)

	// A plain repeated click in the same second still creates nothing.
	if _, err := s.Disparar(ctx, "diario", now, nil, nil); err != nil {
		t.Fatal(err)
	}
	assertOneRunOneItem(t, pool, ctx)
}

func assertOneRunOneItem(t *testing.T, pool *postgres.Pool, ctx context.Context) {
	t.Helper()
	var runs, items, queued int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM runs),
		(SELECT count(*) FROM queue_items),
		(SELECT count(*) FROM runs WHERE status = 'queued')`).Scan(&runs, &items, &queued); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || items != 1 || queued != 1 {
		t.Fatalf("want 1 run, 1 queue item, 1 queued run; got %d, %d, %d", runs, items, queued)
	}
}
