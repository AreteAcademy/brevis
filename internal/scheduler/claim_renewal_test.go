package scheduler_test

import (
	"context"
	"testing"
	"time"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
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
