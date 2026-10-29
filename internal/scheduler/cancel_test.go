package scheduler_test

import (
	"context"
	"errors"
	"fmt"
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

// cancelable reads the requests often, and would retry a failure at once:
// a canceled run that went down the failure path would run again inside the
// test.
var cancelable = scheduler.Config{
	Worker: "w", MaxAttempts: 3, Interval: 10 * time.Millisecond,
	BackoffBase: time.Millisecond, Visibility: time.Minute, RecoveryInterval: time.Hour,
	CancelPoll: 20 * time.Millisecond,
}

// runDispatcher runs d until the test ends, and waits for it to finish.
func runDispatcher(t *testing.T, d *scheduler.Dispatcher) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})
}

func waitForStatus(t *testing.T, repo *postgres.RunRepo, id uuid.UUID, want dom.Status) dom.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := repo.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run is %s after 5s; wanted %s", r.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func queueEmpty(t *testing.T, q *queue.Queue) {
	t.Helper()
	if pending, claimed, _ := q.Size(context.Background()); pending+claimed != 0 {
		t.Errorf("%d item(s) left in the queue", pending+claimed)
	}
}

// A RUNNING RUN THAT IS ASKED TO STOP IS CANCELED, AND NOT RETRIED. Its steps
// are told why -- ErrCanceled, which every executor reads as "stop" -- and
// none of its attempts is spent: nothing failed.
func TestARunningRunAskedToStopIsCanceledNotRetried(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "to-cancel")

	started := make(chan struct{})
	var runs atomic.Int32
	var cause atomic.Value
	d := scheduler.New(cancelable, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		if runs.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		return fmt.Errorf("the run was interrupted: %w", ctx.Err())
	}, noLog())
	d.Channel = alerts.ChannelSlack
	runDispatcher(t, d)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executar never started")
	}
	asked := time.Now()
	if got, err := repo.RequestCancel(context.Background(), id, "ana"); err != nil || got.Outcome != postgres.CancelRequested {
		t.Fatalf("RequestCancel = %+v, %v", got, err)
	}

	r := waitForStatus(t, repo, id, dom.StatusCanceled)
	if took := time.Since(asked); took > time.Second {
		t.Errorf("canceled %s after the request; the poll is 20ms", took)
	}
	time.Sleep(100 * time.Millisecond) // a retry would have started by now
	if n := runs.Load(); n != 1 {
		t.Errorf("executar ran %d times; a canceled run is not retried", n)
	}
	if c, _ := cause.Load().(error); !errors.Is(c, execution.ErrCanceled) {
		t.Errorf("the run's context was ended by %v, wanted ErrCanceled", c)
	}
	if r.Attempt != 0 || r.Err != "canceled by ana" || r.FinishedAt == nil {
		t.Errorf("run = attempt %d, err %q, ended %v; wanted 0, \"canceled by ana\", set", r.Attempt, r.Err, r.FinishedAt)
	}
	if rows := raised(t, pool, id); len(rows) != 0 {
		t.Errorf("%d alert(s) for a canceled run", len(rows))
	}
	queueEmpty(t, q)
}

// A request that reached a run whose item was already claimed -- or that a
// dead dispatcher's item carried back to the queue -- is honored before the
// run starts: it never runs.
func TestARunAskedToStopBeforeItStartsNeverRuns(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "asked-early")
	if _, err := pool.Exec(context.Background(),
		`UPDATE runs SET cancel_requested_at = now(), canceled_by = 'cli' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	d := scheduler.New(cancelable, q, repo, func(context.Context, uuid.UUID) error {
		runs.Add(1)
		return nil
	}, noLog())
	runDispatcher(t, d)

	r := waitForStatus(t, repo, id, dom.StatusCanceled)
	if n := runs.Load(); n != 0 {
		t.Errorf("executar ran %d times", n)
	}
	if r.Err != "canceled by cli" || r.StartedAt != nil {
		t.Errorf("run = err %q, started %v; wanted \"canceled by cli\", never started", r.Err, r.StartedAt)
	}
	time.Sleep(50 * time.Millisecond)
	queueEmpty(t, q)
}

// A run that has already ended and still has an item -- canceled between a
// claim and its start, say -- is not released back to be claimed again: that
// was a loop with no wait in it.
func TestAnEndedRunsItemLeavesTheQueue(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "ended")
	if err := repo.Transicionar(context.Background(), id, dom.StatusCanceled); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	d := scheduler.New(cancelable, q, repo, func(context.Context, uuid.UUID) error {
		runs.Add(1)
		return nil
	}, noLog())
	runDispatcher(t, d)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if pending, claimed, _ := q.Size(context.Background()); pending+claimed == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ended run's item is still in the queue after 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("executar ran %d times", n)
	}
}

// A run asked to stop whose dispatcher died is canceled by the sweep, and not
// failed and retried as an orphan.
func TestAnOrphanAskedToStopIsCanceled(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	ctx := context.Background()
	id := queuedRun(t, pool, q, "orphan")
	if err := repo.Transicionar(ctx, id, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE queue_items SET reivindicado_em = now() - interval '1 hour', reivindicado_por = 'dead'
		WHERE run_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.RequestCancel(ctx, id, "ana"); err != nil || got.Outcome != postgres.CancelRequested {
		t.Fatalf("RequestCancel = %+v, %v", got, err)
	}

	d := scheduler.New(cancelable, q, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())
	if n, err := d.RecoverOrphans(ctx); err != nil || n != 1 {
		t.Fatalf("RecoverOrphans = %d, %v", n, err)
	}

	r, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != dom.StatusCanceled || r.Attempt != 0 {
		t.Errorf("orphan = %s at attempt %d; wanted canceled, no attempt spent", r.Status, r.Attempt)
	}
	queueEmpty(t, q)
}

// pollCounter counts how many times a run's request was read.
type pollCounter struct {
	*postgres.RunRepo
	id    uuid.UUID
	reads atomic.Int32
}

func (p *pollCounter) CancelRequested(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	for _, id := range ids {
		if id == p.id {
			p.reads.Add(1)
		}
	}
	return p.RunRepo.CancelRequested(ctx, ids)
}

// A run is stopped once. A step that takes a while to die -- a pod being
// deleted, a process ignoring SIGTERM -- is not asked again every poll, and
// the log does not say "canceling" every two seconds until it does.
func TestARunIsStoppedOnce(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "slow-to-stop")
	counter := &pollCounter{RunRepo: repo, id: id}

	started := make(chan struct{})
	d := scheduler.New(cancelable, q, counter, func(ctx context.Context, _ uuid.UUID) error {
		close(started)
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // fifteen polls
		return ctx.Err()
	}, noLog())
	runDispatcher(t, d)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executar never started")
	}
	before := counter.reads.Load()
	if _, err := repo.RequestCancel(context.Background(), id, "ana"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, repo, id, dom.StatusCanceled)
	if n := counter.reads.Load() - before; n > 2 {
		t.Errorf("the request was read %d times after it was made; a run is stopped once", n)
	}
}
