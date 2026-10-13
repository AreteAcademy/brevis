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
	landed, err := (&sink{}).Reconcile(context.Background(), errors.New("staging bucket missing"))
	if landed || err != nil {
		t.Errorf("Reconcile = %v, %v; want false, nil", landed, err)
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
