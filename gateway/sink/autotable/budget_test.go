package autotable

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// shares records what budget each table's sink was handed, and lets a named
// table block until its own context closes.
type shares struct {
	mu     sync.Mutex
	order  []string
	budget map[string]time.Duration
	landed map[string]bool
	block  map[string]bool
	// hold, when set, is how long a blocked table keeps going AFTER its own
	// share has closed -- long enough to outlast the caller's window too.
	hold time.Duration
}

func newShares(block ...string) *shares {
	s := &shares{
		budget: map[string]time.Duration{},
		landed: map[string]bool{},
		block:  map[string]bool{},
	}
	for _, t := range block {
		s.block[t] = true
	}
	return s
}

func (s *shares) of(table string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget[table]
}

type watcher struct {
	table string
	log   *shares
}

func (w watcher) Describe() string { return "watcher:" + w.table }

func (w watcher) Write(ctx context.Context, _ []sdk.Envelope) (int64, error) {
	w.log.mu.Lock()
	w.log.order = append(w.log.order, w.table)
	if d, ok := ctx.Deadline(); ok {
		w.log.budget[w.table] = time.Until(d)
	}
	blocked := w.log.block[w.table]
	w.log.mu.Unlock()

	if blocked {
		<-ctx.Done()
		time.Sleep(w.log.hold)
		return 0, ctx.Err()
	}
	w.log.mu.Lock()
	w.log.landed[w.table] = true
	w.log.mu.Unlock()
	return 1, nil
}

// routerWatching builds a router whose every table lands in a watcher.
func routerWatching(t *testing.T, log *shares) *router {
	t.Helper()
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	sinks.MustRegister("watch", func(b gateway.Build) (gateway.Sinker, error) {
		return watcher{table: b.Sink.Table, log: log}, nil
	})
	built, err := gateway.BuildSink(gateway.Build{
		Ctx: context.Background(),
		Sink: gateway.Sink{
			Type: gateway.SinkAutoTable,
			Into: &gateway.Sink{Type: "watch"},
		},
		Sinks: sinks,
		Meta:  gateway.NewMemoryMetastore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return built.(*router)
}

func batchFor(tables ...string) []gateway.Envelope {
	out := make([]gateway.Envelope, 0, len(tables))
	for i, table := range tables {
		out = append(out, gateway.Envelope{Payload: map[string]any{
			FieldTable:     table,
			FieldUniqueKey: "id",
			FieldData:      map[string]any{"id": string(rune('A' + i))},
		}})
	}
	return out
}

// A slow table must not spend the budget the next one needs. [#42]
//
// The reporter's second half: the router writes N tables inside one Write,
// under one deadline. In the window they measured, `id_bdc` took 12 s and
// `id_engine_verification` 16 s out of the same sixty — so a table that takes
// all of it leaves the ones after it nothing, and they are dead-lettered for
// a slowness that was not theirs.
func TestASlowTableDoesNotSpendTheNextOnes(t *testing.T) {
	log := newShares("a_slow")
	r := routerWatching(t, log)

	budget := 400 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	// Sorted, so `a_slow` is written first and `z_after` has to survive it.
	_, err := r.Write(ctx, batchFor("a_slow", "z_after"))
	if err == nil {
		t.Fatal("the slow table's failure was not reported")
	}
	if !strings.Contains(err.Error(), "a_slow") {
		t.Errorf("the error is %q and does not name the table that failed", err)
	}
	if strings.Contains(err.Error(), "z_after") {
		t.Errorf("the error blames z_after, which did nothing: %v", err)
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if !log.landed["z_after"] {
		t.Error("z_after never landed. One table exhausting its own budget " +
			"must not abandon the ones behind it: that is the same shape as " +
			"issue #34, where one table's error spent the rate limit the " +
			"others needed")
	}
	if got := log.budget["a_slow"]; got > budget*6/10 {
		t.Errorf("a_slow was handed %s of a %s budget: the first table cannot "+
			"be given the share of one that has not run yet", got, budget)
	}
}

// The share is what is LEFT divided by the tables still to go, and that is
// observable: four fast tables see their shares grow, because each one's
// leftover is redistributed rather than wasted.
func TestEachTableGetsAShareOfWhatIsLeft(t *testing.T) {
	log := newShares()
	r := routerWatching(t, log)

	budget := 400 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	if _, err := r.Write(ctx, batchFor("tbl_a", "tbl_b", "tbl_c", "tbl_d")); err != nil {
		t.Fatal(err)
	}

	// A quarter to the first, and near the whole remainder to the last. Equal
	// shares computed ONCE would hand the last one a quarter as well, with
	// three quarters of the budget unspent and nobody allowed to use it.
	near := func(table string, want time.Duration) {
		t.Helper()
		got := log.of(table)
		if got < want*8/10 || got > want*12/10 {
			t.Errorf("%s was handed %s, want about %s", table, got, want)
		}
	}
	near("tbl_a", budget/4)
	near("tbl_b", budget/3)
	near("tbl_c", budget/2)
	near("tbl_d", budget)
}

// With no deadline on the caller there is no share to divide, and none is
// invented.
//
// The router is not only reached from `send`: it is an ordinary Sinker, and a
// caller that set no deadline asked for none. Inventing one here would be a
// timeout nobody configured, in a path nobody looks at.
func TestWithNoDeadlineNoneIsImposed(t *testing.T) {
	log := newShares()
	r := routerWatching(t, log)

	if _, err := r.Write(context.Background(), batchFor("tbl_a", "tbl_b")); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tbl_a", "tbl_b"} {
		if got := log.of(table); got != 0 {
			t.Errorf("%s was handed a %s deadline out of a context that has "+
				"none", table, got)
		}
	}
}

// A table that fails for a reason that is NOT its budget still stops the run.
//
// Narrow on purpose. A deadline says nothing about the other tables; any other
// error says the destination disagreed with what we believed, and the designed
// response to that is to drop the cached belief and stop -- the next batch
// re-reads the catalogue. Continuing past it would re-ask a destination that
// has just told us we are wrong, once per table.
func TestAnErrorThatIsNotTheBudgetStillStopsTheRun(t *testing.T) {
	log := newShares()
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	refused := errors.New("the destination disagreed")
	sinks.MustRegister("watch", func(b gateway.Build) (gateway.Sinker, error) {
		if b.Sink.Table == "a_refuses" {
			return refuser{err: refused}, nil
		}
		return watcher{table: b.Sink.Table, log: log}, nil
	})
	built, err := gateway.BuildSink(gateway.Build{
		Ctx: context.Background(),
		Sink: gateway.Sink{
			Type: gateway.SinkAutoTable,
			Into: &gateway.Sink{Type: "watch"},
		},
		Sinks: sinks,
		Meta:  gateway.NewMemoryMetastore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := built.(*router)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Write(ctx, batchFor("a_refuses", "z_after")); !errors.Is(err, refused) {
		t.Fatalf("the error is %v, want the destination's own", err)
	}
	if log.landed["z_after"] {
		t.Error("z_after was written after the destination said we were " +
			"wrong about the table before it: the cache is dropped and the " +
			"next batch re-reads the catalogue, which is the designed response")
	}
}

type refuser struct{ err error }

func (refuser) Describe() string { return "refuser" }
func (r refuser) Write(context.Context, []sdk.Envelope) (int64, error) {
	return 0, r.err
}

// The CALLER's window closing is a different thing from a table's share
// closing, and the run stops.
//
// THE FIRST VERSION OF THIS TEST PROVED NOTHING, and it is worth the lines.
// It closed the caller's window with `cancel()`, which comes back as
// `context.Canceled` -- a different branch, the one every non-deadline error
// takes, which stops the run anyway. So the guard could be deleted and the
// test still passed. The window has to close by its own DEADLINE, which is
// how a drain window actually ends, and then the error is
// `DeadlineExceeded` and indistinguishable from a table overrunning its share
// unless something asks whose deadline it was.
//
// Without the guard, every table behind the first is attempted with nothing
// left, fails instantly, and is named in an error as though it had overrun
// something of its own.
func TestTheCallersWindowClosingStopsTheRun(t *testing.T) {
	log := newShares("a_holds")
	// Past its own share AND past what is left of the caller's window.
	log.hold = 300 * time.Millisecond
	r := routerWatching(t, log)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := r.Write(ctx, batchFor("a_holds", "m_after", "z_after"))
	if err == nil {
		t.Fatal("a closed window was not reported")
	}
	if !strings.Contains(err.Error(), "a_holds") {
		t.Errorf("the error is %q and does not name where it stopped", err)
	}
	for _, blameless := range []string{"m_after", "z_after"} {
		if strings.Contains(err.Error(), blameless) {
			t.Errorf("the error blames %s, which was never given a window to "+
				"overrun: %v", blameless, err)
		}
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.order) != 1 {
		t.Errorf("%v tables were attempted: once the caller's window is shut "+
			"there is nothing left to write the rest with, and attempting "+
			"them only adds names to an error", log.order)
	}
}
