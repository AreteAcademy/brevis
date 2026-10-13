package bigquery

import (
	"context"
	"errors"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
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
