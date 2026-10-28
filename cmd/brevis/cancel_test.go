package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

type cancelSpy struct {
	got    uuid.UUID
	actor  string
	calls  int
	result postgres.CancelResult
	err    error
}

func (s *cancelSpy) RequestCancel(_ context.Context, id uuid.UUID, actor string) (postgres.CancelResult, error) {
	s.calls++
	s.got, s.actor = id, actor
	return s.result, s.err
}

// The answer says which of the three happened: a run canceled now, one whose
// dispatcher still has to stop it, and one that had already ended -- with how.
func TestTheCancelSaysWhatHappened(t *testing.T) {
	id := uuid.New()
	for _, c := range []struct {
		result postgres.CancelResult
		says   string
	}{
		{postgres.CancelResult{Outcome: postgres.CancelDone, Status: dom.StatusCanceled}, "canceled"},
		{postgres.CancelResult{Outcome: postgres.CancelRequested, Status: dom.StatusRunning}, "cancel requested"},
		{postgres.CancelResult{Outcome: postgres.CancelAlreadyEnded, Status: dom.StatusSuccess}, "already ended: success"},
	} {
		spy := &cancelSpy{result: c.result}
		got, err := cancelRun(context.Background(), spy, id.String())
		if err != nil {
			t.Fatalf("%s: %v", c.says, err)
		}
		if !strings.HasPrefix(got, c.says) || !strings.Contains(got, id.String()) {
			t.Errorf("%v: said %q, wanted %q and the run's id", c.result.Outcome, got, c.says)
		}
		if spy.got != id || spy.actor != "cli" {
			t.Errorf("asked for %s as %q; wanted %s as cli", spy.got, spy.actor, id)
		}
	}
}

func TestCancelingAnUnknownRunFails(t *testing.T) {
	spy := &cancelSpy{err: fmt.Errorf("run x: %w", postgres.ErrRunNotFound)}
	if _, err := cancelRun(context.Background(), spy, uuid.NewString()); !errors.Is(err, postgres.ErrRunNotFound) {
		t.Errorf("err = %v, wanted ErrRunNotFound", err)
	}
}

// A malformed id is refused before the database is asked anything.
func TestCancelingAMalformedIDFails(t *testing.T) {
	spy := &cancelSpy{}
	if _, err := cancelRun(context.Background(), spy, "not-a-run"); err == nil || !strings.Contains(err.Error(), "not-a-run") {
		t.Errorf("err = %v, wanted one naming the id", err)
	}
	if spy.calls != 0 {
		t.Errorf("the database was asked %d time(s)", spy.calls)
	}
}
