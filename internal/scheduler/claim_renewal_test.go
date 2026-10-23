package scheduler_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// lostClaim is a dispatcher whose claim is taken mid-run: the window and the
// heartbeat of longRun, a sweep that never fires on its own -- the test takes
// the claim by hand -- one attempt, and alerting on, so a stale attempt that
// wrongly wrote its failure would also raise.
var lostClaim = scheduler.Config{
	Worker: "w", MaxAttempts: 1, Interval: 10 * time.Millisecond,
	Visibility: 300 * time.Millisecond, RecoveryInterval: time.Hour,
}

// heldBy reads the run's queue item claim; "" when there is no item.
func heldBy(t *testing.T, pool *postgres.Pool, id uuid.UUID) string {
	t.Helper()
	var by *string
	err := pool.QueryRow(context.Background(),
		`SELECT reivindicado_por FROM queue_items WHERE run_id = $1`, id).Scan(&by)
	if err != nil || by == nil {
		return ""
	}
	return *by
}

// untilExecuted runs the dispatcher until executar has returned once, then
// stops it -- Run waits for the bookkeeping, so whatever the attempt was going
// to write has been written when this returns.
func untilExecuted(t *testing.T, d *scheduler.Dispatcher, returned <-chan struct{}) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("executar never returned")
	}
	stop()
	<-done
}

// assertUntouched: the run and its item are exactly as the new holder has them.
// The stale attempt wrote nothing -- no transition, no error, no attempt, no
// alert, and it did not finish the item.
func assertUntouched(t *testing.T, pool *postgres.Pool, id uuid.UUID) {
	t.Helper()
	r, err := postgres.NewRunRepo(pool).Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != dom.StatusRunning || r.Attempt != 0 || r.Err != "" {
		t.Errorf("run = %s, attempt %d, err %q; the stale attempt wrote into a run it no longer holds",
			r.Status, r.Attempt, r.Err)
	}
	if by := heldBy(t, pool, id); by != "other" {
		t.Errorf("the item is held by %q; wanted it left to the new holder", by)
	}
	if rows := raised(t, pool, id); len(rows) != 0 {
		t.Errorf("the stale attempt raised %d alerts", len(rows))
	}
}

// AC4: the sweep took the claim while the run ran. The heartbeat finds out,
// the attempt is cancelled -- its pod or process stops -- and it writes nothing.
func TestALostClaimCancelsTheRunAndWritesNothing(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "lost")

	returned := make(chan struct{})
	var cancelledAfter atomic.Int64
	d := scheduler.New(lostClaim, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		defer close(returned)
		if _, err := pool.Exec(ctx, `
			UPDATE queue_items SET reivindicado_em = now() + interval '1 hour', reivindicado_por = 'other'
			WHERE run_id = $1`, id); err != nil {
			return err
		}
		taken := time.Now()
		select {
		case <-ctx.Done():
			cancelledAfter.Store(int64(time.Since(taken)))
			return ctx.Err()
		case <-time.After(threeWindows):
			return nil
		}
	}, noLog())
	d.Channel = alerts.ChannelSlack

	untilExecuted(t, d, returned)
	if after := time.Duration(cancelledAfter.Load()); after == 0 {
		t.Error("the run went on after its claim was taken; it has to be cancelled")
	} else if after > lostClaim.Visibility {
		t.Errorf("the run was cancelled %s after its claim was taken; the heartbeat is every %s",
			after, lostClaim.Visibility/3)
	}
	assertUntouched(t, pool, id)
}

// The claim can be lost after the last renewal and before executar returns,
// which the heartbeat cannot see. The last write is fenced too: one more
// renewal, and nothing written if it says the claim is gone -- whatever the
// run's outcome was.
func TestAClaimLostAsTheRunEndsWritesNothing(t *testing.T) {
	for name, outcome := range map[string]error{
		"success": nil,
		"failure": errors.New(`step "run": exited with code 2`),
	} {
		t.Run(name, func(t *testing.T) {
			pool := testDB(t)
			repo := postgres.NewRunRepo(pool)
			q := queue.New(pool.Pool)
			id := queuedRun(t, pool, q, "ending-"+name)

			returned := make(chan struct{})
			d := scheduler.New(lostClaim, q, repo, func(ctx context.Context, _ uuid.UUID) error {
				defer close(returned)
				if _, err := pool.Exec(ctx, `
					UPDATE queue_items SET reivindicado_em = now() + interval '1 hour', reivindicado_por = 'other'
					WHERE run_id = $1`, id); err != nil {
					return err
				}
				return outcome
			}, noLog())
			d.Channel = alerts.ChannelSlack

			untilExecuted(t, d, returned)
			assertUntouched(t, pool, id)
		})
	}
}

// The last renewal failing says nothing about the claim, so the run's outcome
// is written as it always was. Skipping it would leave a finished run
// `running`, for the sweep to fail and run a second time.
func TestALastRenewalErrorStillWritesTheOutcome(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "last-blip")

	returned := make(chan struct{})
	d := scheduler.New(lostClaim, q, repo, func(context.Context, uuid.UUID) error {
		close(returned)
		return nil
	}, noLog())
	d.Renew = func(context.Context, queue.Item) (bool, error) {
		return false, errors.New("connection reset by peer")
	}

	untilExecuted(t, d, returned)
	r, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != dom.StatusSuccess {
		t.Errorf("run = %s; a renewal error is not a lost claim, and the success is written", r.Status)
	}
	if pending, claimed, _ := q.Size(context.Background()); pending+claimed != 0 {
		t.Errorf("the queue kept %d items", pending+claimed)
	}
}

// Every line the dispatcher writes names the process that wrote it.
// "requeued" without it says a run went back to the queue, and not from which
// of three schedulers.
func TestTheDispatchersLinesNameItsWorker(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "named")

	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	cfg := longRun
	cfg.Worker = "brevis-scheduler-0-42"

	var calls atomic.Int32
	d := scheduler.New(cfg, q, repo, func(context.Context, uuid.UUID) error {
		if calls.Add(1) == 1 {
			return errors.New(`step "run": exited with code 2`)
		}
		return nil
	}, log)

	dispatchUntilSettled(t, d, repo, id, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.Contains(out.String(), "msg=requeued") {
		t.Fatalf("the run was never requeued, so there is nothing to read:\n%s", out.String())
	}
	for _, l := range lines {
		if !strings.Contains(l, "worker=brevis-scheduler-0-42") {
			t.Errorf("a line that does not name its worker: %q", l)
		}
	}
}

// And a lost claim says so -- the one cancel after which nothing may be left
// running.
func TestALostClaimCancelsTheRunWithItsReason(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)
	id := queuedRun(t, pool, q, "cause-lost")

	returned := make(chan struct{})
	var cause atomic.Value
	d := scheduler.New(lostClaim, q, repo, func(ctx context.Context, _ uuid.UUID) error {
		defer close(returned)
		if _, err := pool.Exec(ctx, `
			UPDATE queue_items SET reivindicado_em = now() + interval '1 hour', reivindicado_por = 'other'
			WHERE run_id = $1`, id); err != nil {
			return err
		}
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		return ctx.Err()
	}, noLog())

	untilExecuted(t, d, returned)
	if got, _ := cause.Load().(error); !errors.Is(got, execution.ErrClaimLost) {
		t.Errorf("the run's context ended with cause %v; wanted ErrClaimLost", got)
	}
}
