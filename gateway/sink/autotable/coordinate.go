package autotable

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

// How long a claim on one table's shape lasts.
//
// One second, and the number is bounded from both sides:
//
//	long enough   to cover ONE ALTER. That is all it has to cover: once the
//	              winner's column is in the catalogue, a loser proceeding finds
//	              nothing to plan and issues no DDL at all. The window protects
//	              the in-flight moment, not the whole quota period.
//	short enough  that a loser gets through on its own retries. The pipe makes
//	              four attempts with a jittered backoff -- roughly 1.7 to 3.5
//	              seconds in total -- so a window it cannot outlast is a batch
//	              in the dead letter.
//
// That second half was three seconds first, and two replicas meeting one new
// field put the loser's batch in the dead letter: its four attempts were spent
// inside the window. A debounce that can bury a batch is not a debounce.
const ClaimWindow = time.Second

// ErrClaimed is a batch that met a shape somebody else is already altering
// for.
//
// Returned rather than waited on: waiting would hold a worker for the window,
// and there are four of them. The pipe retries the batch, which releases the
// worker and puts the batch back in the queue -- and by the second attempt the
// winner's column is in the catalogue and this batch needs no DDL at all.
var ErrClaimed = errors.New("another replica is altering this table for this shape; retrying")

// coordinator is the metastore, used for the three things this package needs.
//
// A thin wrapper and not a second abstraction: it exists so the key layout
// lives in ONE place. Two callers building `exists:app_orders` by hand is how
// a cache starts answering for a key nobody wrote.
type coordinator struct {
	store  gateway.Metastore
	stream string
	ttl    time.Duration
}

// knows reports whether the table is believed to exist.
//
// The rule that keeps this safe: it is an OPTIMISATION and never the
// authority. If it says the table is there and the write fails, the write is
// right -- the entry is dropped and the next batch re-reads. Without that, a
// table recreated outside the gateway leaves every replica lying until the TTL.
func (c *coordinator) knows(ctx context.Context, table string) (exists bool, known bool) {
	v, ok, err := c.store.Get(ctx, c.key("exists", table))
	if err != nil || !ok {
		// A metastore that is down is a metastore that knows nothing. It must
		// not be able to fail a write: the destination is the source of truth
		// and a cache miss costs a round trip, which is the right price for
		// Redis being unreachable.
		return false, false
	}
	return v == "1", true
}

func (c *coordinator) learn(ctx context.Context, table string, exists bool) {
	v := "0"
	if exists {
		v = "1"
	}
	_ = c.store.Put(ctx, c.key("exists", table), v, c.ttl)
}

// forget drops what is known, for when the destination disagrees.
//
// This is the rule that keeps the cache safe, and it is a rule with teeth only
// if something calls it: if the cache says the table is there and the write
// fails, THE WRITE IS RIGHT. Without this a table dropped or recreated outside
// the gateway leaves every replica believing a shape that is gone, until the
// TTL runs out on each of them separately.
func (c *coordinator) forget(ctx context.Context, table string) {
	_ = c.store.Put(ctx, c.key("exists", table), "0", time.Second)
}

// claimDDL asks to be the one that alters this table for this shape.
//
// It is a DEBOUNCE, not a lock: whoever gets it runs the DDL, whoever does not
// simply lets the write proceed and be retried, because by then the winner's
// column is probably in the catalogue. There is nothing to release and no
// lease to renew, so a replica that dies holding one stalls nobody.
//
// With `memory` every replica gets its own answer and this debounces nothing,
// which is exactly what `redis` and `memcached` are for.
func (c *coordinator) claimDDL(ctx context.Context, table, shape string) bool {
	got, err := c.store.Claim(ctx, c.key("ddl", table+":"+shape), ClaimWindow)
	if err != nil {
		// Unreachable: behave as if the claim were ours. Refusing here would
		// stop every write on a cache being down, which is the one thing a
		// cache must never do.
		return true
	}
	return got
}

// admit counts one table creation in the rolling hour and reports whether it
// fits under the limit.
//
// Shared, it bounds the DEPLOYMENT. With `memory` it bounds a process, and
// four replicas admit four times the number -- which is why the docs say so
// and why this exists.
func (c *coordinator) admit(ctx context.Context, max int, now time.Time) error {
	// One key per hour, expiring after two: a fixed window that rolls by
	// changing keys rather than by pruning a list, which is the only shape
	// memcached can count in.
	key := c.key("new", now.UTC().Format("2006-01-02T15"))
	n, err := c.store.Incr(ctx, key, 2*time.Hour)
	if err != nil {
		// A counter that cannot be read must not stop ingestion. The naming
		// rules still apply; only the rate limit is relaxed, and it is a
		// circuit breaker rather than a quota.
		return nil
	}
	if n > int64(max) {
		return fmt.Errorf("this stream has already created %d tables this hour, and "+
			"`naming.max_new_per_hour` is %d. A limit reached here is a producer to "+
			"look at, not a number to raise", n-1, max)
	}
	return nil
}

func (c *coordinator) key(kind, name string) string {
	return "brevis:gw:" + c.stream + ":" + kind + ":" + name
}
