package execution_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// fates is an executor that gives each node the outcome the test names:
// "refused" is a start that ran nothing, the way a pool where every agent is
// draining answers (#146); "fails" exits 1; anything else succeeds. It counts
// the starts each node was given.
type fates struct {
	of     map[string]string
	mu     sync.Mutex
	starts map[string]int
}

func (*fates) Name() string                         { return "fates" }
func (*fates) Cancel(context.Context, string) error { return nil }

func (f *fates) Execute(_ context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	f.mu.Lock()
	if f.starts == nil {
		f.starts = map[string]int{}
	}
	f.starts[t.NodeID]++
	f.mu.Unlock()

	switch f.of[t.NodeID] {
	case "refused":
		return nil, fmt.Errorf("the agent on pool refused the step: %w", execution.ErrNotStarted)
	case "fails":
		ch := make(chan execution.Event, 2)
		ch <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}
		ch <- execution.Event{Kind: execution.EventFailed, NodeID: t.NodeID, ExitCode: 1,
			Message: "the step exited with code 1"}
		close(ch)
		return ch, nil
	}
	ch := make(chan execution.Event, 2)
	ch <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}
	ch <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID}
	close(ch)
	return ch, nil
}

func (f *fates) startsOf(node string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[node]
}

func runFates(f *fates, w wf.Workflow) error {
	return app.Runner{
		Processo: f, Report: &coletor{},
		MaxAttempts: 3, BackoffBase: time.Millisecond,
	}.Run(context.Background(), w)
}

// A step nobody started spends none of its own attempts: asking a pool that is
// draining again, three times over a few seconds, only burns them. The run
// says why, and the dispatcher decides what to do with it.
func TestAStepNotStartedIsNotRetriedByTheRunner(t *testing.T) {
	f := &fates{of: map[string]string{"load": "refused"}}
	err := runFates(f, wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "load", Run: "load.sh"}}})

	if !errors.Is(err, execution.ErrNotStarted) {
		t.Fatalf("Run = %v, wanted ErrNotStarted", err)
	}
	if got := f.startsOf("load"); got != 1 {
		t.Errorf("the step was started %d times, wanted 1", got)
	}
}

// A run whose only failure is a step nobody started was not started either, as
// far as anything that ran is concerned: its sibling succeeded and is not run
// again.
func TestARunWhoseOnlyFailureWasNotStartedIsNotStarted(t *testing.T) {
	f := &fates{of: map[string]string{"load": "refused", "notify": "succeeds"}}
	err := runFates(f, wf.Workflow{Slug: "w", Nodes: []wf.Node{
		{ID: "load", Run: "load.sh"}, {ID: "notify", Run: "notify.sh"},
	}})

	if !errors.Is(err, execution.ErrNotStarted) {
		t.Fatalf("Run = %v, wanted ErrNotStarted", err)
	}
}

// A step that FAILED beside one nobody started makes the run a failure: it is
// what the attempt and the alert are for, and a run sent back to wait for a
// drain would hide it.
func TestARealFailureBesideANotStartedStepIsTheRunsFailure(t *testing.T) {
	f := &fates{of: map[string]string{"load": "refused", "check": "fails"}}
	err := runFates(f, wf.Workflow{Slug: "w", Nodes: []wf.Node{
		{ID: "load", Run: "load.sh"}, {ID: "check", Run: "check.sh"},
	}})

	if err == nil || errors.Is(err, execution.ErrNotStarted) {
		t.Fatalf("Run = %v, wanted the failure of check", err)
	}
}

// The same across levels: the step nobody started is in the first, the one
// that failed in the second, on a branch that did not depend on it.
func TestARealFailureInALaterLevelIsTheRunsFailure(t *testing.T) {
	f := &fates{of: map[string]string{"load": "refused", "extract": "succeeds", "check": "fails"}}
	err := runFates(f, wf.Workflow{Slug: "w",
		Nodes: []wf.Node{
			{ID: "load", Run: "load.sh"}, {ID: "extract", Run: "extract.sh"}, {ID: "check", Run: "check.sh"},
		},
		Edges: []wf.Edge{{From: "extract", To: "check"}},
	})

	if err == nil || errors.Is(err, execution.ErrNotStarted) {
		t.Fatalf("Run = %v, wanted the failure of check", err)
	}
}
