package bigquery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// The BigQuery sink is the one whose writes outlive the call: a load job keeps
// running after the gateway stops waiting. Issue #85.
var _ gateway.Reconciler = (*sink)(nil)

// Only a write that named its job can be asked about. Anything else -- a
// refused schema, a staging bucket that does not exist -- did not land, and
// asking BigQuery about it would be a call with no question.
func TestAFailureWithNoJobIsNotReconciled(t *testing.T) {
	cause := errors.New("staging bucket missing")
	rec, err := (&sink{}).Reconcile(context.Background(), cause)
	if rec.Landed || err != nil || rec.Cause != cause {
		t.Errorf("Reconcile = %+v, %v; want not landed, the same cause", rec, err)
	}
}

// Every row of a batch shares its one load job.
func TestEveryRowNamesTheBatchJob(t *testing.T) {
	cause := fmt.Errorf("x: %w", &sdk.LoadJobError{JobID: "brevis_x_g1", State: sdk.JobRunning, Err: errors.New("wait")})
	je, ok := (&sink{}).JobOf(cause, map[string]any{"id": "1"})
	if !ok || je.JobID != "brevis_x_g1" {
		t.Errorf("JobOf = %+v, %v", je, ok)
	}
	if _, ok := (&sink{}).JobOf(errors.New("refused"), nil); ok {
		t.Error("a failure with no job named one")
	}
}

var _ gateway.JobNamer = (*sink)(nil)

// Landed means finished AND without an error. A job that failed is done too,
// and counting it as landed would drop the batch on the floor: not written,
// not buried. What comes back is what the dead-letter row will say.
func TestWhatTheJobBecameIsWhatThePipeDoes(t *testing.T) {
	asked := &sdk.LoadJobError{JobID: "j", Location: "US", State: sdk.JobRunning, Err: errors.New("wait for load job: deadline")}
	stateOf := func(rec gateway.Reconciliation) sdk.JobState {
		var je *sdk.LoadJobError
		if !errors.As(rec.Cause, &je) {
			return ""
		}
		return je.State
	}
	cases := []struct {
		name    string
		report  sdk.JobReport
		landed  bool
		stamped sdk.JobState // "" when the row names no job
	}{
		{"running", sdk.JobReport{State: sdk.JobRunning}, false, sdk.JobRunning},
		{"failed", sdk.JobReport{State: sdk.JobDone, Failed: errors.New("bad row")}, false, sdk.JobDone},
		{"landed", sdk.JobReport{State: sdk.JobDone}, true, sdk.JobDone},
		{"absent", sdk.JobReport{State: sdk.JobAbsent}, false, ""},
	}
	for _, c := range cases {
		rec := reconciliationOf(asked, c.report)
		if rec.Landed != c.landed || stateOf(rec) != c.stamped {
			t.Errorf("%s: landed=%v stamped=%q, want %v %q", c.name, rec.Landed, stateOf(rec), c.landed, c.stamped)
		}
	}
	if asked.State != sdk.JobRunning {
		t.Error("the error that was asked about was changed in place")
	}
}

// The pipe's key reaches the SDK, which names the load job after it.
func TestTheBatchKeyReachesTheLoad(t *testing.T) {
	ctx := gateway.WithBatchKey(context.Background(), "batch-1")
	if got := (&sink{}).options(ctx).BatchKey; got != "batch-1" {
		t.Errorf("BatchKey = %q, want batch-1", got)
	}
}
