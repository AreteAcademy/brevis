package scheduler_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// oneAttempt is the policy that makes a shutdown's cost impossible to miss: on
// the ordinary failure path an interrupted run is out of attempts at once, and
// raises an alert.
var oneAttempt = scheduler.Config{
	Worker: "w", MaxAttempts: 1, Interval: 10 * time.Millisecond,
	Visibility: time.Minute, RecoveryInterval: time.Hour,
}

// shutDownMidRun starts the dispatcher, waits for executar to begin, then
// shuts the dispatcher down the way a SIGTERM does and waits for it to finish
// -- Run waits for the bookkeeping, so it has all been written on return.
func shutDownMidRun(t *testing.T, d *scheduler.Dispatcher, started <-chan struct{}) {
	t.Helper()
	ctx, sigterm := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("executar never started")
	}
	sigterm()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the dispatcher never finished shutting down")
	}
}

// #122: a run the scheduler stopped on its way down was not ruled out -- it
// was stopped. It goes back to the queue at once, for the next scheduler, with
// its attempts untouched, no alert, and an error that says what happened.
func TestARunInterruptedByAShutdownIsRequeuedForFree(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "interrupted")

	started := make(chan struct{})
	d := scheduler.New(oneAttempt, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		close(started)
		<-ctx.Done()
		// What the runner returns: ErrInterrupted, which the dispatcher knows
		// only as an error wrapping the context's.
		return fmt.Errorf("the run was interrupted: %w", ctx.Err())
	}, noLog())
	d.Channel = alerts.ChannelSlack

	shutDownMidRun(t, d, started)

	r, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != dom.StatusQueued || r.Attempt != 0 {
		t.Errorf("run = %s at attempt %d; wanted queued, no attempt spent", r.Status, r.Attempt)
	}
	if !strings.Contains(r.Err, "interrupted") || !strings.Contains(r.Err, "shut down") {
		t.Errorf("the run's error %q does not say the scheduler shut down", r.Err)
	}
	if rows := raised(t, pool, id); len(rows) != 0 {
		t.Errorf("a shutdown raised %d alerts", len(rows))
	}
	var available bool
	if err := pool.QueryRow(context.Background(), `
		SELECT reivindicado_em IS NULL AND disponivel_em <= now()
		FROM queue_items WHERE run_id = $1`, id).Scan(&available); err != nil {
		t.Fatalf("the run has no queue item: %v", err)
	}
	if !available {
		t.Error("the item is not available now; the next scheduler would wait for it")
	}
}

// D4: a run whose last step finished as the SIGTERM arrived DID succeed. The
// shutdown is read from the runner's answer, not from the context alone.
func TestARunThatFinishedAsTheShutdownCameStillSucceeds(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "finished")

	started := make(chan struct{})
	d := scheduler.New(oneAttempt, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		close(started)
		<-ctx.Done()
		return nil
	}, noLog())

	shutDownMidRun(t, d, started)

	r, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != dom.StatusSuccess {
		t.Errorf("run = %s; a run that finished is a success, shutdown or not", r.Status)
	}
}

// A run that genuinely FAILED -- its graph finished, with a step's exit code --
// fails like any other, even if the shutdown arrived before the bookkeeping.
// Only an interruption is free.
func TestARunThatFailedAsTheShutdownCameSpendsItsAttempt(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "failed")

	started := make(chan struct{})
	d := scheduler.New(oneAttempt, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		close(started)
		<-ctx.Done()
		return fmt.Errorf(`level 1: step "load": exited with code 2`)
	}, noLog())

	shutDownMidRun(t, d, started)

	r, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != dom.StatusFailed || r.Attempt != 1 {
		t.Errorf("run = %s at attempt %d; a real failure spends its attempt", r.Status, r.Attempt)
	}
}

// And an error that wraps context.Canceled while NOBODY is shutting down is a
// failure too: requeuing it for free would loop a broken run forever.
func TestACanceledErrorWithoutAShutdownIsAFailure(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "not-a-shutdown")

	d := scheduler.New(oneAttempt, q, repo, func(context.Context, uuid.UUID) error {
		return fmt.Errorf("a client gave up: %w", context.Canceled)
	}, noLog())

	// The dispatcher stays up until the run has its outcome: stopping it as
	// soon as executar returned would BE a shutdown, and test the other case.
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	defer func() { stop(); <-done }()

	var r dom.Run
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		var err error
		if r, err = repo.Get(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		// The attempt is spent AFTER `failed` is written; reading in between
		// would see a failure not yet counted.
		if r.Attempt > 0 || r.Status == dom.StatusSuccess {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.Status != dom.StatusFailed || r.Attempt != 1 {
		t.Errorf("run = %s at attempt %d; wanted an ordinary failure", r.Status, r.Attempt)
	}
}
