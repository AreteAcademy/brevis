package run

import (
	"errors"
	"testing"
	"time"
)

// TestARunIsNeverSkipped.
//
// `skipped` describes a STEP whose trigger rule was not satisfied. A RUN has no
// such notion: one whose every step was skipped is a SUCCESS, because nothing
// failed. The run's transition map is what enforces it, and this is the
// assertion that would catch somebody adding an edge because a compiler asked
// for one.
func TestARunIsNeverSkipped(t *testing.T) {
	for _, from := range []Status{
		StatusCreated, StatusQueued, StatusRunning,
		StatusSuccess, StatusFailed, StatusRetrying, StatusCanceled,
	} {
		if err := Validate(from, StatusSkipped); err == nil {
			t.Errorf("%s -> skipped was allowed; a run is never skipped", from)
		}
	}
	// And the run's machine does not recognise the two step-only states at
	// all, which is the stronger statement: they are not merely unreachable,
	// they are not part of a run's vocabulary.
	for _, s := range []Status{StatusPending, StatusSkipped} {
		if err := Validate(s, StatusRunning); err == nil || !isUnknown(err) {
			t.Errorf("a run's machine treats %q as one of its own: %v", s, err)
		}
	}
}

// A step's machine, on the other hand, knows both -- and says "invalid
// transition" rather than "unknown state", or the error sends whoever reads it
// looking for a typo.
func TestAStepsMachineKnowsThem(t *testing.T) {
	err := ValidateStep(StatusSkipped, StatusRunning)
	if err == nil {
		t.Fatal("skipped -> running was allowed")
	}
	var invalid ErrInvalidTransition
	if !errors.As(err, &invalid) {
		t.Errorf("skipped reads as an unknown state to a step: %v", err)
	}
}

// TestOnlyAStepThatNeverStartedCanBeSkipped.
//
// It is the rule that keeps the state honest. A step already running was given
// its chance; whatever happens to it after that is success, failure or a
// cancellation. Allowing running -> skipped would let a step that did work be
// recorded as though it had not.
func TestOnlyAStepThatNeverStartedCanBeSkipped(t *testing.T) {
	if err := ValidateStep(StatusPending, StatusSkipped); err != nil {
		t.Errorf("a step that never started could not be skipped: %v", err)
	}
	for _, from := range []Status{StatusRunning, StatusSuccess, StatusFailed, StatusCanceled} {
		if err := ValidateStep(from, StatusSkipped); err == nil {
			t.Errorf("%s -> skipped was allowed; that step had already started", from)
		}
	}
}

// A skipped step is done being decided about. It is terminal for the same
// reason success is: this run will not reconsider it, and a screen showing it
// as still pending would be waiting for something that is not coming.
func TestSkippedIsTerminal(t *testing.T) {
	if !StatusSkipped.TerminalStep() {
		t.Error("skipped is not terminal, so a skipped step reads as still to come")
	}
	// And it is not a RUN's terminal state, because it is not a run state at
	// all. Terminal() and TerminalStep() answer different questions, and the
	// place they differ is FAILED: final for a step, a retry away for a run.
	if StatusFailed.Terminal() {
		t.Error("a failed RUN reads as terminal; it can still retry")
	}
	if !StatusFailed.TerminalStep() {
		t.Error("a failed STEP does not read as finished, so `all_done` waits forever")
	}
	for _, to := range []Status{StatusQueued, StatusRunning, StatusSuccess, StatusFailed} {
		if StatusSkipped.CanStepGo(to) {
			t.Errorf("skipped -> %s is allowed", to)
		}
	}
}

// TestASkippedStepIsNotASuccessAndNotAFailure is the distinction the state
// exists for. Folding it into either one is the mistake this whole change is
// against: a step whose upstream failed did not fail, and saying it succeeded
// is a lie that reaches the run's own status.
func TestASkippedStepIsNotASuccessAndNotAFailure(t *testing.T) {
	if StatusSkipped == StatusSuccess || StatusSkipped == StatusFailed {
		t.Fatal("skipped is an alias for something else")
	}
	// It is stamped like any other end. A step with no end time reads as still
	// running, and a skipped one is not running.
	now := time.Now()
	tr := &TaskRun{Status: StatusPending}
	if err := tr.Transition(StatusSkipped, now); err != nil {
		t.Fatalf("a step could not be marked skipped: %v", err)
	}
	if tr.FinishedAt == nil {
		t.Error("a skipped step has no end time, so it reads as still running")
	}
	if tr.StartedAt != nil {
		t.Error("a skipped step has a start time, and it never started")
	}
}

// A step's retries are a loop inside the runner, with a row per attempt. The
// step itself never sits in a state that means "about to try again", and
// leaving RETRYING out of its machine is what says so.
func TestAStepDoesNotRetryThroughTheStateMachine(t *testing.T) {
	if err := ValidateStep(StatusFailed, StatusRetrying); err == nil {
		t.Error("a step transitioned to retrying; its retries are the runner's loop")
	}
	// The run's does, and that difference is the reason there are two maps.
	if err := Validate(StatusFailed, StatusRetrying); err != nil {
		t.Errorf("a run can no longer retry: %v", err)
	}
}

func isUnknown(err error) bool {
	var invalid ErrInvalidTransition
	return !errors.As(err, &invalid)
}
