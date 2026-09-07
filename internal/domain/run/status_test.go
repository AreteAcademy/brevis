package run

import (
	"errors"
	"testing"
	"time"
)

// O caminho feliz da secao 7: created -> queued -> running -> success.
func TestCaminhoFeliz(t *testing.T) {
	r := &Run{Status: StatusCreated}
	agora := time.Now()

	for _, s := range []Status{StatusQueued, StatusRunning, StatusSuccess} {
		if err := r.Transition(s, agora); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
	}
	if r.StartedAt == nil || r.FinishedAt == nil {
		t.Error("running e success devem carimbar os tempos")
	}
}

// O ciclo de retry: failed -> retrying -> queued, e de volta a running.
func TestTheRetryCycle(t *testing.T) {
	r := &Run{Status: StatusRunning}
	agora := time.Now()

	for _, s := range []Status{StatusFailed, StatusRetrying, StatusQueued, StatusRunning} {
		if err := r.Transition(s, agora); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
	}
}

// Re-queueing clears the stamps: the new attempt does not inherit the previous
// one's times, otherwise the reported duration would be another run's.
func TestRequeueingClearsTheStamps(t *testing.T) {
	agora := time.Now()
	r := &Run{Status: StatusCreated}
	_ = r.Transition(StatusQueued, agora)
	_ = r.Transition(StatusRunning, agora)
	_ = r.Transition(StatusFailed, agora)
	_ = r.Transition(StatusRetrying, agora)
	_ = r.Transition(StatusQueued, agora)

	if r.StartedAt != nil || r.FinishedAt != nil {
		t.Error("re-queued has to clear the previous attempt's stamps")
	}
}

// Section 7 requires an invalid transition to return an error -- not to be
// ignored.
func TestInvalidTransitionsAreRefused(t *testing.T) {
	casos := []struct{ de, para Status }{
		{StatusSuccess, StatusRunning},  // terminal does not come back
		{StatusCanceled, StatusQueued},  // terminal does not come back
		{StatusCreated, StatusRunning},  // does not skip the queue
		{StatusCreated, StatusSuccess},  // does not skip the run
		{StatusQueued, StatusSuccess},   // does not finish without running
		{StatusRunning, StatusRetrying}, // only a failure goes to retry
		{StatusFailed, StatusRunning},   // a retry goes through the queue
	}
	for _, c := range casos {
		err := Validate(c.de, c.para)
		if err == nil {
			t.Errorf("%s -> %s should have been refused", c.de, c.para)
			continue
		}
		var inv ErrInvalidTransition
		if !errors.As(err, &inv) {
			t.Errorf("%s -> %s returned %T, wanted ErrInvalidTransition", c.de, c.para, err)
		}
	}
}

// FAILED is not terminal: what decides whether there is another attempt is the
// retry policy, not the state machine.
func TestFailedIsNotTerminal(t *testing.T) {
	if StatusFailed.Terminal() {
		t.Error("failed cannot be terminal -- it goes to retrying")
	}
	if !StatusSuccess.Terminal() || !StatusCanceled.Terminal() {
		t.Error("success e canceled sao terminais")
	}
}

// Cancelling has to be possible from any active state -- and only from active
// ones.
func TestCancelamento(t *testing.T) {
	for _, de := range []Status{StatusCreated, StatusQueued, StatusRunning, StatusRetrying} {
		if err := Validate(de, StatusCanceled); err != nil {
			t.Errorf("it should be possible to cancel from %s: %v", de, err)
		}
	}
	if err := Validate(StatusSuccess, StatusCanceled); err == nil {
		t.Error("what already succeeded is not cancelled")
	}
}

func TestEstadoDesconhecido(t *testing.T) {
	if err := Validate(Status("inventado"), StatusQueued); err == nil {
		t.Error("expected an error for an unknown state")
	}
}
