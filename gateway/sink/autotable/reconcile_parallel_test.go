package autotable

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// slowAsk is a table whose question takes a while to answer, and counts how
// many questions are open at once across every table that shares `open`.
type slowAsk struct {
	table      string
	took       time.Duration
	landed     bool
	fails      error
	open, peak *atomic.Int64
}

func (s slowAsk) Describe() string                                     { return "slow:" + s.table }
func (s slowAsk) Write(context.Context, []sdk.Envelope) (int64, error) { return 0, nil }
func (s slowAsk) Reconcile(ctx context.Context, cause error) (gateway.Reconciliation, error) {
	n := s.open.Add(1)
	defer s.open.Add(-1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-time.After(s.took):
	case <-ctx.Done():
		return gateway.Reconciliation{Cause: cause}, ctx.Err()
	}
	if s.fails != nil {
		return gateway.Reconciliation{Cause: cause}, s.fails
	}
	return gateway.Reconciliation{Landed: s.landed, Cause: cause}, nil
}

func starved(n int, took time.Duration, landed bool) (*batchError, *atomic.Int64) {
	var open, peak atomic.Int64
	be := &batchError{err: errors.New("tables starved"), failed: map[string]tableFailure{}}
	for i := 0; i < n; i++ {
		table := fmt.Sprintf("tbl_%02d", i)
		be.failed[table] = tableFailure{
			sink: slowAsk{table: table, took: took, landed: landed, open: &open, peak: &peak},
			err:  &sdk.LoadJobError{Table: table, JobID: "job_" + table, State: sdk.JobRunning},
		}
	}
	return be, &peak
}

// #87: the starved tables of one batch were asked about one after another
// inside a single 5 s reconcileTimeout. Eight tables at 300 ms each spent
// 2.4 s of it, and enough of them would bury a batch whose jobs all landed.
// Asked together, eight answer in about one latency.
func TestStarvedTablesAreAskedTogether(t *testing.T) {
	be, _ := starved(8, 300*time.Millisecond, true)
	start := time.Now()
	rec, err := (&router{}).Reconcile(context.Background(), be)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Landed {
		t.Error("eight tables that all landed did not reconcile as landed")
	}
	if took > 900*time.Millisecond {
		t.Errorf("eight 300 ms questions took %s; asked together they take about one", took)
	}
}

// Together, but not all at once: each question is a jobs.get against the
// project's API quota, and a batch of 94 tables must not open 94.
func TestNoMoreThanTheFanoutAreAskedAtOnce(t *testing.T) {
	be, peak := starved(40, 50*time.Millisecond, true)
	if _, err := (&router{}).Reconcile(context.Background(), be); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p > reconcileFanout {
		t.Errorf("%d questions were open at once; the fanout is %d", p, reconcileFanout)
	}
	if p := peak.Load(); p < 2 {
		t.Errorf("the peak was %d: the tables were asked one at a time", p)
	}
}

// One table not known to have landed buries the batch, as before; and one
// table whose question failed fails the reconcile, after the others answered.
func TestOneTableDecidesTheBatchAsBefore(t *testing.T) {
	be, _ := starved(4, 10*time.Millisecond, true)
	for table, f := range be.failed {
		s := f.sink.(slowAsk)
		s.landed = false
		be.failed[table] = tableFailure{sink: s, err: f.err}
		break
	}
	rec, err := (&router{}).Reconcile(context.Background(), be)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Landed {
		t.Error("a batch with a table that did not land reconciled as landed")
	}

	be, _ = starved(4, 10*time.Millisecond, true)
	for table, f := range be.failed {
		s := f.sink.(slowAsk)
		s.fails = errors.New("bigquery answered 503")
		be.failed[table] = tableFailure{sink: s, err: f.err}
		break
	}
	if _, err := (&router{}).Reconcile(context.Background(), be); err == nil {
		t.Error("a table whose question failed did not fail the reconcile")
	}
}
