package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// stranded builds the state a crash leaves behind: a run walked through
// `path` and its queue item either still claimed by a dead worker (claimed) or
// waiting in the queue.
func stranded(t *testing.T, key string, claimed bool, path ...dom.Status) (
	*postgres.RunRepo, *queue.Queue, uuid.UUID,
) {
	t.Helper()
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: key, Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if claimed {
		if _, err := q.Claim(ctx, "dead-worker", 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, to := range path {
		if err := repo.Transicionar(ctx, r.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	return repo, q, r.ID
}

func sweep(t *testing.T, repo *postgres.RunRepo, q *queue.Queue) {
	t.Helper()
	d := scheduler.New(scheduler.Config{Worker: "alive", MaxAttempts: 3, Visibility: time.Nanosecond},
		q, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())
	if _, err := d.RecoverOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func wantRetried(t *testing.T, repo *postgres.RunRepo, q *queue.Queue, id uuid.UUID, attempt int) {
	t.Helper()
	got, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != dom.StatusQueued || got.Attempt != attempt {
		t.Errorf("run is %s with attempt %d; wanted queued with attempt %d", got.Status, got.Attempt, attempt)
	}
	if pending, claimed, _ := q.Size(context.Background()); pending != 1 || claimed != 0 {
		t.Errorf("queue has %d pending, %d claimed; wanted the item handed back (1, 0)", pending, claimed)
	}
}

// The attempt rolled back with the alert (see Attempt), so the run sits
// `failed` with its item claimed. The sweep has to finish that failure -- the
// "retry" the rollback counts on -- and not delete the item.
func TestTheSweepRetriesARunWhoseAttemptRolledBack(t *testing.T) {
	repo, q, id := stranded(t, "rolled-back", true,
		dom.StatusQueued, dom.StatusRunning, dom.StatusFailed)
	sweep(t, repo, q)
	wantRetried(t, repo, q, id, 1)
}

// Killed between `queued` and the item's release: the retry is fully recorded,
// only the hand-back is missing. Failing it again is an invalid edge and used
// to end in Done.
func TestTheSweepOnlyReleasesARunAlreadyRequeued(t *testing.T) {
	repo, q, id := stranded(t, "half-requeued", true,
		dom.StatusQueued, dom.StatusRunning, dom.StatusFailed, dom.StatusRetrying, dom.StatusQueued)
	sweep(t, repo, q)
	wantRetried(t, repo, q, id, 0) // no second attempt spent
}

// Same, one step earlier: `retrying` with the item still claimed.
func TestTheSweepFinishesARunLeftRetrying(t *testing.T) {
	repo, q, id := stranded(t, "left-retrying", true,
		dom.StatusQueued, dom.StatusRunning, dom.StatusFailed, dom.StatusRetrying)
	sweep(t, repo, q)
	wantRetried(t, repo, q, id, 0)
}

// A `failed` run whose item is waiting made process() hit an invalid
// `failed -> running`, and, `failed` not being terminal, release it with no
// delay: a hot loop. It has to be finished and handed back with a backoff.
func TestAFailedRunWithAWaitingItemIsNotClaimedInALoop(t *testing.T) {
	repo, q, id := stranded(t, "hot-loop", false,
		dom.StatusQueued, dom.StatusRunning, dom.StatusFailed)

	var ran int
	d := scheduler.New(scheduler.Config{
		Worker: "w", MaxConcorrente: 1, Interval: 20 * time.Millisecond,
		MaxAttempts: 3, BackoffBase: time.Hour, BackoffMax: time.Hour,
	}, q, repo, func(context.Context, uuid.UUID) error { ran++; return nil }, noLog())

	ctx, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer stop()
	_ = d.Run(ctx)

	if ran != 0 {
		t.Errorf("the executor ran %d times for a run that was already failed", ran)
	}
	got, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != dom.StatusQueued || got.Attempt != 1 {
		t.Errorf("run is %s with attempt %d; wanted queued with attempt 1", got.Status, got.Attempt)
	}
}
