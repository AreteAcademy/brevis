package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
)

// The gateway reads the job off a write's error with the public name. It must
// reach through the wrapping a sink adds.
func TestTheLoadJobIsReadableThroughWrapping(t *testing.T) {
	inner := &sdk.LoadJobError{Table: "events", JobID: "brevis_x_g1", State: sdk.JobRunning,
		Err: fmt.Errorf("wait for load job: %w", context.DeadlineExceeded)}
	err := fmt.Errorf("table events: %w", inner)

	var je *sdk.LoadJobError
	if !errors.As(err, &je) || je.JobID != "brevis_x_g1" {
		t.Fatalf("the job is lost behind the wrapping: %v", err)
	}
}
