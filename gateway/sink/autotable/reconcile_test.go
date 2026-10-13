package autotable

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// jobSink is a table whose load job may outlive its share: a slow table gives
// up with the job named on the error, and Reconcile answers with what the job
// became server side.
type jobSink struct {
	table  string
	slow   bool          // gives up when its context ends
	ignore time.Duration // or overstays it by this much, past the caller's window too
	landed bool          // what its job became
}

func (j jobSink) Describe() string { return "job:" + j.table }
func (j jobSink) Write(ctx context.Context, b []sdk.Envelope) (int64, error) {
	switch {
	case j.ignore > 0:
		time.Sleep(j.ignore)
	case j.slow:
		<-ctx.Done()
	default:
		return int64(len(b)), nil
	}
	return 0, &sdk.LoadJobError{Table: j.table, JobID: "job_" + j.table, State: sdk.JobRunning,
		Err: fmt.Errorf("wait for load job: %w", context.DeadlineExceeded)}
}
func (j jobSink) Reconcile(_ context.Context, cause error) (gateway.Reconciliation, error) {
	var je *sdk.LoadJobError
	if !errors.As(cause, &je) {
		return gateway.Reconciliation{Cause: cause}, nil
	}
	now := *je
	if j.landed {
		now.State = sdk.JobDone
	}
	return gateway.Reconciliation{Landed: j.landed, Cause: &now}, nil
}

func routerOverJobs(t *testing.T, tables map[string]jobSink) *router {
	t.Helper()
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	sinks.MustRegister("job", func(b gateway.Build) (gateway.Sinker, error) {
		if s, ok := tables[b.Sink.Table]; ok {
			return s, nil
		}
		return jobSink{table: b.Sink.Table}, nil
	})
	built, err := gateway.BuildSink(gateway.Build{
		Ctx:   context.Background(),
		Sink:  gateway.Sink{Type: gateway.SinkAutoTable, Into: &gateway.Sink{Type: "job"}},
		Sinks: sinks,
		Meta:  gateway.NewMemoryMetastore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return built.(*router)
}

func writeWithin(t *testing.T, r *router, budget time.Duration, tables ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	_, err := r.Write(ctx, batchFor(tables...))
	if err == nil {
		t.Fatal("the slow table reported success")
	}
	return err
}

// Issue #85 was a batch of one table: its share was the whole window, and the
// job it stopped waiting for landed. That batch is delivered.
func TestALoneTableWhoseJobLandedReconciles(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_a": {table: "tbl_a", slow: true, landed: true}})
	err := writeWithin(t, r, 50*time.Millisecond, "tbl_a")

	rec, rerr := r.Reconcile(context.Background(), err)
	if rerr != nil || !rec.Landed {
		t.Errorf("Reconcile = %+v, %v; the only table landed", rec, rerr)
	}
}

func TestAStarvedTableWhoseJobLandedCompletesTheBatch(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_b": {table: "tbl_b", slow: true, landed: true}})
	err := writeWithin(t, r, 150*time.Millisecond, "tbl_a", "tbl_b", "tbl_c")

	if rec, rerr := r.Reconcile(context.Background(), err); rerr != nil || !rec.Landed {
		t.Errorf("Reconcile = %+v, %v; every table landed", rec, rerr)
	}
}

func TestAStarvedTableStillRunningKeepsTheBatchUnlanded(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_b": {table: "tbl_b", slow: true, landed: false}})
	err := writeWithin(t, r, 150*time.Millisecond, "tbl_a", "tbl_b", "tbl_c")

	if rec, _ := r.Reconcile(context.Background(), err); rec.Landed {
		t.Error("a batch with a table still loading was reported as landed")
	}
}

// When the CALLER's window closes on a table, the tables after it are never
// written. Its job landing does not make the batch landed.
func TestTablesNeverWrittenKeepTheBatchUnlanded(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_a": {table: "tbl_a", ignore: 120 * time.Millisecond, landed: true}})
	err := writeWithin(t, r, 60*time.Millisecond, "tbl_a", "tbl_b")

	if rec, _ := r.Reconcile(context.Background(), err); rec.Landed {
		t.Error("tbl_b was never written, and the batch was reported as landed")
	}
}

// The pipe finds the question by type assertion; the router must answer it.
var _ gateway.Reconciler = (*router)(nil)

// In a batch of many tables, each row names the job of ITS table, and a row of
// a table that landed names none.
func TestEachBuriedRowNamesTheJobOfItsTable(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_b": {table: "tbl_b", slow: true}})
	err := writeWithin(t, r, 150*time.Millisecond, "tbl_a", "tbl_b")
	batch := batchFor("tbl_a", "tbl_b")

	if je, ok := r.JobOf(err, batch[1].Payload.(map[string]any)); !ok || je.JobID != "job_tbl_b" {
		t.Errorf("tbl_b's row: JobOf = %+v, %v", je, ok)
	}
	if je, ok := r.JobOf(err, batch[0].Payload.(map[string]any)); ok {
		t.Errorf("tbl_a landed, and its row names job %s", je.JobID)
	}
}

var _ gateway.JobNamer = (*router)(nil)

// keyRecorder notes the batch key each table's write was handed.
type keyRecorder struct {
	table string
	seen  *[]string
}

func (k keyRecorder) Describe() string { return "keys:" + k.table }
func (k keyRecorder) Write(ctx context.Context, b []sdk.Envelope) (int64, error) {
	*k.seen = append(*k.seen, gateway.BatchKeyOf(ctx))
	return int64(len(b)), nil
}

// #85 on its own path: the router stamps brevis_received_at when it writes,
// so a retry a minute later has other bytes. What names the load job must not
// change with them -- every attempt reaches the table with the pipe's key.
func TestARetryReachesEachTableWithTheBatchKey(t *testing.T) {
	var seen []string
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	sinks.MustRegister("keys", func(b gateway.Build) (gateway.Sinker, error) {
		return keyRecorder{table: b.Sink.Table, seen: &seen}, nil
	})
	built, err := gateway.BuildSink(gateway.Build{
		Ctx:   context.Background(),
		Sink:  gateway.Sink{Type: gateway.SinkAutoTable, Into: &gateway.Sink{Type: "keys"}},
		Sinks: sinks,
		Meta:  gateway.NewMemoryMetastore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := built.(*router)
	clock := time.Date(2026, 10, 13, 0, 6, 24, 0, time.UTC)
	r.now = func() time.Time { return clock }

	ctx := gateway.WithBatchKey(context.Background(), "batch-1")
	for range 2 {
		if _, err := r.Write(ctx, batchFor("tbl_a")); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Minute)
	}
	if len(seen) != 2 || seen[0] != "batch-1" || seen[1] != "batch-1" {
		t.Errorf("the table's writes carried keys %q, want batch-1 twice", seen)
	}
}

// A batch buried whole still says which of its tables landed: a row of a table
// whose job finished names it as done, so a replay skips it instead of writing
// it twice.
func TestARowOfATableThatLandedSaysSo(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{
		"tbl_a": {table: "tbl_a", slow: true, landed: true},
		"tbl_b": {table: "tbl_b", slow: true, landed: false},
	})
	err := writeWithin(t, r, 100*time.Millisecond, "tbl_a", "tbl_b")
	rec, rerr := r.Reconcile(context.Background(), err)
	if rerr != nil || rec.Landed {
		t.Fatalf("Reconcile = %+v, %v", rec, rerr)
	}
	batch := batchFor("tbl_a", "tbl_b")
	a, _ := r.JobOf(rec.Cause, batch[0].Payload.(map[string]any))
	b, _ := r.JobOf(rec.Cause, batch[1].Payload.(map[string]any))
	if a == nil || a.State != sdk.JobDone {
		t.Errorf("tbl_a landed and its row says %+v", a)
	}
	if b == nil || b.State != sdk.JobRunning {
		t.Errorf("tbl_b is still loading and its row says %+v", b)
	}
}
