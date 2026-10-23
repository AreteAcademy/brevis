package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// claimOne queues one run and claims it, returning the item exactly as the
// dispatcher holds it.
func claimOne(t *testing.T, pool *postgres.Pool, q *queue.Queue, key string) queue.Item {
	t.Helper()
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: key, IdempotencyKey: key, Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	items, err := q.Claim(ctx, "w", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("claimed %d items, wanted 1", len(items))
	}
	return items[0]
}

// age moves a claim an hour into the past, the way fifteen minutes of a long
// run would, and returns the item as its holder now knows it.
func age(t *testing.T, pool *postgres.Pool, it queue.Item) queue.Item {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		UPDATE queue_items SET reivindicado_em = reivindicado_em - interval '1 hour'
		WHERE id = $1 RETURNING reivindicado_em`, it.ID).Scan(&it.ClaimedAt); err != nil {
		t.Fatal(err)
	}
	return it
}

// reclaim is the sweep and another claim, collapsed: the item is held again,
// under a claim its first holder does not know.
func reclaim(t *testing.T, pool *postgres.Pool, it queue.Item) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE queue_items SET reivindicado_em = now() + interval '1 second', reivindicado_por = 'other'
		WHERE id = $1`, it.ID); err != nil {
		t.Fatal(err)
	}
}

func claimedBy(t *testing.T, pool *postgres.Pool, it queue.Item) (by *string, renewed *time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT reivindicado_por, renovado_em FROM queue_items WHERE id = $1`, it.ID).
		Scan(&by, &renewed); err != nil {
		t.Fatal(err)
	}
	return by, renewed
}

// #121: the sweep took a run its own scheduler was still running, because
// nothing said the claim was alive. A renewed claim is not an orphan, however
// old the claim itself is.
func TestARenewedClaimIsNotRecovered(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	alive := age(t, pool, claimOne(t, pool, q, "alive"))
	dead := age(t, pool, claimOne(t, pool, q, "dead"))

	held, err := q.Renew(ctx, alive)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("renewing a claim nobody took reports it lost")
	}

	items, err := q.Recover(ctx, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != dead.ID {
		t.Fatalf("recovered %+v; wanted only the unrenewed item %d", items, dead.ID)
	}
}

// The other half of the fence: once the claim belongs to somebody else, the
// first holder's renewal matches nothing, and nothing is how it learns.
func TestRenewingAClaimTakenSinceReportsItLost(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "taken")
	reclaim(t, pool, it)

	held, err := q.Renew(ctx, it)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("a stale holder's renewal reports the claim still held")
	}
	if _, renewed := claimedBy(t, pool, it); renewed != nil {
		t.Error("a stale holder's renewal wrote renovado_em on somebody else's claim")
	}
}

// The issue's `success -> success`: the first attempt finished and deleted the
// item the second attempt was running. A stale Done deletes nothing.
func TestAStaleDoneLeavesTheNewClaimAlone(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "done")
	reclaim(t, pool, it)

	if err := q.Done(ctx, it); err != nil {
		t.Fatal(err)
	}
	if by, _ := claimedBy(t, pool, it); by == nil || *by != "other" {
		t.Errorf("after a stale Done the item is claimed by %v; wanted it still held by other", by)
	}
}

// And a stale Release does not hand somebody else's run back to the queue.
func TestAStaleReleaseLeavesTheNewClaimAlone(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "release")
	reclaim(t, pool, it)

	if err := q.Release(ctx, it, 0); err != nil {
		t.Fatal(err)
	}
	if by, _ := claimedBy(t, pool, it); by == nil || *by != "other" {
		t.Errorf("after a stale Release the item is claimed by %v; wanted it still held by other", by)
	}
}

// A new claim never inherits the previous holder's heartbeat: if it did, the
// sweep would measure the new holder's silence from somebody else's renewal.
// Claim does not clear it -- an unclaimed item has none, because both ways out
// of a claim, Release and the sweep, clear it. This and the test after it are
// what hold that up.
func TestANewClaimStartsWithNoRenewal(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "again")
	if _, err := q.Renew(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err := q.Release(ctx, it, 0); err != nil {
		t.Fatal(err)
	}
	if _, renewed := claimedBy(t, pool, it); renewed != nil {
		t.Fatal("Release kept the previous holder's renewal")
	}

	again, err := q.Claim(ctx, "w2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].ID != it.ID {
		t.Fatalf("claimed %+v; wanted item %d back", again, it.ID)
	}
	if !again[0].ClaimedAt.After(it.ClaimedAt) {
		t.Errorf("the new claim's ClaimedAt %s is not after the old one's %s",
			again[0].ClaimedAt, it.ClaimedAt)
	}
}

// The sweep's way out clears the heartbeat too. A holder that renewed and then
// went silent leaves a renewal behind, and the next claim must not start from it.
func TestTheSweepLeavesNoRenewalBehind(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "silent")
	if _, err := q.Renew(ctx, it); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Recover(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, renewed := claimedBy(t, pool, it); renewed != nil {
		t.Error("the sweep kept the silent holder's renewal")
	}
}

// The sweep leaves an item unclaimed, and what it hands back says so: a zero
// ClaimedAt. The orphan path's own Done is fenced on that -- it removes the
// item while nobody has claimed it again...
func TestTheOrphanPathFinishesAnItemNobodyClaimedAgain(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "orphan")
	items, err := q.Recover(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != it.ID || !items[0].ClaimedAt.IsZero() {
		t.Fatalf("recovered %+v; wanted item %d with no claim", items, it.ID)
	}
	if err := q.Done(ctx, items[0]); err != nil {
		t.Fatal(err)
	}
	if pending, claimed, _ := q.Size(ctx); pending+claimed != 0 {
		t.Errorf("the orphan path's Done left %d items", pending+claimed)
	}
}

// ...and leaves it alone once another dispatcher has.
func TestTheOrphanPathLeavesAnItemClaimedAgain(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	q := queue.New(pool.Pool)

	it := claimOne(t, pool, q, "orphan-taken")
	items, err := q.Recover(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("recovered %d items, wanted 1", len(items))
	}
	reclaim(t, pool, it)

	if err := q.Release(ctx, items[0], time.Minute); err != nil {
		t.Fatal(err)
	}
	if by, _ := claimedBy(t, pool, it); by == nil || *by != "other" {
		t.Errorf("the orphan path's Release took the item from its new holder (by %v)", by)
	}
}

// queuedRun is a run the way the scheduler leaves it: queued, with its item.
func queuedRun(t *testing.T, pool *postgres.Pool, q *queue.Queue, key string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: key, IdempotencyKey: key, Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// The issue's timings, shrunk: a window of 300ms, a sweep every 20ms, a run
// three windows long, and a backoff short enough that a requeued run WOULD run
// again inside the test. Before the heartbeat this ran twice.
var longRun = scheduler.Config{
	Worker: "w", MaxAttempts: 3, Interval: 10 * time.Millisecond,
	BackoffBase: 10 * time.Millisecond,
	Visibility:  300 * time.Millisecond, RecoveryInterval: 20 * time.Millisecond,
}

const threeWindows = 900 * time.Millisecond

// dispatchUntilSettled runs the dispatcher until the run reaches a status no
// attempt leaves -- success, or failed with the attempts spent -- and then a
// little longer, so a second execution that was going to happen has.
//
// atSuccess, when given, runs the moment success is first seen. The dispatcher
// writes success only after the heartbeat has stopped, so that is the instant
// "after the run" begins.
func dispatchUntilSettled(t *testing.T, d *scheduler.Dispatcher, repo *postgres.RunRepo,
	id uuid.UUID, atSuccess func()) dom.Run {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	defer func() { stop(); <-done }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := repo.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == dom.StatusSuccess {
			if atSuccess != nil {
				atSuccess()
			}
			time.Sleep(threeWindows)
			r, err = repo.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run never succeeded")
	return dom.Run{}
}

// #121 itself: a run longer than the visibility window, on one live
// dispatcher, executes once.
func TestARunLongerThanTheWindowRunsOnce(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "long")

	var executions atomic.Int32
	d := scheduler.New(longRun, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		executions.Add(1)
		select {
		case <-time.After(threeWindows):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, noLog())

	r := dispatchUntilSettled(t, d, repo, id, nil)
	if n := executions.Load(); n != 1 {
		t.Errorf("the run executed %d times; a live holder's run executes once", n)
	}
	if r.Status != dom.StatusSuccess || r.Attempt != 0 {
		t.Errorf("run = %s at attempt %d; wanted success with no attempt spent", r.Status, r.Attempt)
	}
	if pending, claimed, _ := q.Size(context.Background()); pending+claimed != 0 {
		t.Errorf("the queue kept %d items after the run succeeded", pending+claimed)
	}
}

// The heartbeat lives exactly as long as executar: renewing during it, and not
// a single renewal after it returns.
func TestTheHeartbeatRenewsOnlyWhileTheRunRuns(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "beat")

	var renewals, atSuccess atomic.Int32
	d := scheduler.New(longRun, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		time.Sleep(threeWindows)
		return nil
	}, noLog())
	d.Renew = func(ctx context.Context, it queue.Item) (bool, error) {
		renewals.Add(1)
		return q.Renew(ctx, it)
	}

	dispatchUntilSettled(t, d, repo, id, func() { atSuccess.Store(renewals.Load()) })
	if n := atSuccess.Load(); n < 2 {
		t.Errorf("%d renewals in three windows; the heartbeat is every third of one", n)
	}
	if after, before := renewals.Load(), atSuccess.Load(); after != before {
		t.Errorf("%d renewals after the run ended", after-before)
	}
}

// A renewal the database did not answer says nothing about the claim. It is
// not a lost claim, and the run goes on to succeed once.
func TestARenewalErrorIsNotALostClaim(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "blip")

	var executions, cancelled atomic.Int32
	d := scheduler.New(longRun, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		executions.Add(1)
		select {
		case <-time.After(threeWindows):
			return nil
		case <-ctx.Done():
			cancelled.Add(1)
			return ctx.Err()
		}
	}, noLog())
	var calls atomic.Int32
	d.Renew = func(ctx context.Context, it queue.Item) (bool, error) {
		if calls.Add(1) == 1 {
			return false, errors.New("connection reset by peer")
		}
		return q.Renew(ctx, it)
	}

	r := dispatchUntilSettled(t, d, repo, id, nil)
	if n := executions.Load(); n != 1 || cancelled.Load() != 0 {
		t.Errorf("executed %d times, cancelled %d; one renewal error must change neither",
			n, cancelled.Load())
	}
	if r.Status != dom.StatusSuccess {
		t.Errorf("run = %s; wanted success", r.Status)
	}
}
