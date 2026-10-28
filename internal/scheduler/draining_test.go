package scheduler_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// notStarted is what the runner returns when every agent of a pool refused a
// step for draining (#146).
var notStarted = fmt.Errorf(`level 1: step "load": the agent on pool refused the step: %w`,
	execution.ErrNotStarted)

// dispatching runs the dispatcher until `until` holds, then stops it and waits
// for its bookkeeping.
func dispatching(t *testing.T, d *scheduler.Dispatcher, until func() bool) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	defer func() { stop(); <-done }()
	waitFor(t, until)
}

func runOf(t *testing.T, repo *postgres.RunRepo, id uuid.UUID) dom.Run {
	t.Helper()
	r, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A run nobody started waits for the drain in the queue, not in a slot, and
// spends nothing: no attempt, no alert. It comes back after the configured
// delay -- not at once, which would be a hot loop against a pool still
// draining.
func TestARunNotStartedForADrainIsDeferredForFree(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "deferred")

	var calls atomic.Int32
	cfg := oneAttempt
	cfg.DrainWait, cfg.DrainRequeueDelay = time.Hour, time.Hour
	d := scheduler.New(cfg, q, repo, func(context.Context, uuid.UUID) error {
		calls.Add(1)
		return notStarted
	}, noLog())
	d.Channel = alerts.ChannelSlack

	dispatching(t, d, func() bool {
		return calls.Load() > 0 && runOf(t, repo, id).Status == dom.StatusQueued
	})

	r := runOf(t, repo, id)
	if r.Attempt != 0 {
		t.Errorf("the run is at attempt %d; a drain spends none", r.Attempt)
	}
	if !strings.Contains(r.Err, "draining") || !strings.Contains(r.Err, "without spending an attempt") {
		t.Errorf("the run's error %q does not say it is waiting on a drain", r.Err)
	}
	if rows := raised(t, pool, id); len(rows) != 0 {
		t.Errorf("a drain raised %d alerts", len(rows))
	}
	var later bool
	if err := pool.QueryRow(context.Background(), `
		SELECT reivindicado_em IS NULL AND disponivel_em > now() + interval '50 minutes'
		FROM queue_items WHERE run_id = $1`, id).Scan(&later); err != nil {
		t.Fatalf("the run has no queue item: %v", err)
	}
	if !later {
		t.Error("the item is not held back by the configured delay")
	}
}

// Past the ceiling the drain is no longer an excuse: a pool that has drained
// for longer than anybody asked to wait is a pool that is down, and that is
// what the attempt and the alert are for.
func TestARunWaitingOnADrainPastTheCeilingSpendsItsAttempt(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "past-the-ceiling")
	if _, err := pool.Exec(context.Background(),
		`UPDATE runs SET draining_since = now() - interval '2 hours' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	cfg := oneAttempt
	cfg.DrainWait, cfg.DrainRequeueDelay = time.Hour, time.Millisecond
	d := scheduler.New(cfg, q, repo, func(context.Context, uuid.UUID) error {
		return notStarted
	}, noLog())
	d.Channel = alerts.ChannelSlack

	dispatching(t, d, func() bool { return runOf(t, repo, id).Attempt > 0 })

	r := runOf(t, repo, id)
	if r.Status != dom.StatusFailed || r.Attempt != 1 {
		t.Errorf("run = %s at attempt %d; wanted out of attempts", r.Status, r.Attempt)
	}
	if !strings.Contains(r.Err, "--drain-wait") {
		t.Errorf("the run's error %q does not say the wait ran out", r.Err)
	}
	if rows := raised(t, pool, id); len(rows) != 1 {
		t.Errorf("%d alerts, wanted the one for giving up", len(rows))
	}
}

// With no ceiling the drain is not waited out at all, which is how it was
// before #146: the attempt is spent.
func TestWithoutADrainWaitANotStartedRunSpendsItsAttempt(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "no-drain-wait")

	d := scheduler.New(oneAttempt, q, repo, func(context.Context, uuid.UUID) error {
		return notStarted
	}, noLog())

	dispatching(t, d, func() bool { return runOf(t, repo, id).Attempt > 0 })

	if r := runOf(t, repo, id); r.Status != dom.StatusFailed || r.Attempt != 1 {
		t.Errorf("run = %s at attempt %d; wanted an ordinary failure", r.Status, r.Attempt)
	}
}

// Deferred and then run, the run reads as one attempt: the same run attempt
// both times, so its steps land on the rows they already have instead of
// beside them.
func TestARunDeferredThenRunHasOneRowPerStep(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "deferred-then-run")

	var calls atomic.Int32
	cfg := oneAttempt
	cfg.DrainWait, cfg.DrainRequeueDelay = time.Hour, time.Millisecond
	d := scheduler.New(cfg, q, repo, func(ctx context.Context, runID uuid.UUID) error {
		r, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		if err := repo.IniciarTask(ctx, runID, dom.StepKey{Node: "load", MapIndex: dom.Unmapped},
			r.Attempt+1); err != nil {
			return err
		}
		if calls.Add(1) == 1 {
			return notStarted
		}
		return nil
	}, noLog())

	dispatching(t, d, func() bool { return runOf(t, repo, id).Status == dom.StatusSuccess })

	if got := calls.Load(); got != 2 {
		t.Errorf("executed %d times, wanted the deferral and the run", got)
	}
	if r := runOf(t, repo, id); r.Attempt != 0 {
		t.Errorf("the run is at attempt %d; the deferral spent one", r.Attempt)
	}
	var rows int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM task_runs WHERE run_id = $1`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d step rows, wanted 1", rows)
	}
}
