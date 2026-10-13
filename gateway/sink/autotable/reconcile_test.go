package autotable

import (
	"context"
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
func (j jobSink) Reconcile(context.Context, error) (bool, error) { return j.landed, nil }

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

	landed, rerr := r.Reconcile(context.Background(), err)
	if rerr != nil || !landed {
		t.Errorf("Reconcile = %v, %v; the only table landed", landed, rerr)
	}
}

func TestAStarvedTableWhoseJobLandedCompletesTheBatch(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_b": {table: "tbl_b", slow: true, landed: true}})
	err := writeWithin(t, r, 150*time.Millisecond, "tbl_a", "tbl_b", "tbl_c")

	if landed, rerr := r.Reconcile(context.Background(), err); rerr != nil || !landed {
		t.Errorf("Reconcile = %v, %v; every table landed", landed, rerr)
	}
}

func TestAStarvedTableStillRunningKeepsTheBatchUnlanded(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_b": {table: "tbl_b", slow: true, landed: false}})
	err := writeWithin(t, r, 150*time.Millisecond, "tbl_a", "tbl_b", "tbl_c")

	if landed, _ := r.Reconcile(context.Background(), err); landed {
		t.Error("a batch with a table still loading was reported as landed")
	}
}

// When the CALLER's window closes on a table, the tables after it are never
// written. Its job landing does not make the batch landed.
func TestTablesNeverWrittenKeepTheBatchUnlanded(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{"tbl_a": {table: "tbl_a", ignore: 120 * time.Millisecond, landed: true}})
	err := writeWithin(t, r, 60*time.Millisecond, "tbl_a", "tbl_b")

	if landed, _ := r.Reconcile(context.Background(), err); landed {
		t.Error("tbl_b was never written, and the batch was reported as landed")
	}
}

// The pipe finds the question by type assertion; the router must answer it.
var _ gateway.Reconciler = (*router)(nil)
