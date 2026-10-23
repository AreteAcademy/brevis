package execution_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// mute is an executor that ends a step WITHOUT a verdict: no EventSucceeded,
// no EventFailed, the channel simply closes. It is what the remote executor
// does when its context is cancelled (#122), and what any executor with a bug
// would look like. waitForCancel holds the step until the context ends first.
type mute struct {
	waitForCancel bool
	calls         atomic.Int32
}

func (*mute) Name() string                         { return "mute" }
func (*mute) Cancel(context.Context, string) error { return nil }

func (m *mute) Execute(ctx context.Context, _ execution.TaskExec) (<-chan execution.Event, error) {
	m.calls.Add(1)
	ch := make(chan execution.Event, 1)
	go func() {
		defer close(ch)
		ch <- execution.Event{Kind: execution.EventStarted}
		if m.waitForCancel {
			<-ctx.Done()
		}
	}()
	return ch, nil
}

// endings is a persister that behaves like a database about contexts: a
// write on a context that is already done fails and records nothing. The
// spy it wraps does not look at the context at all, which would hide #122's
// `could not record the end of the step: context canceled`.
type endings struct {
	*spyPersister
	mu     sync.Mutex
	status map[string]dom.Status
	msg    map[string]string
}

func (e *endings) TerminarTask(ctx context.Context, _ uuid.UUID, step dom.StepKey, _ int,
	status dom.Status, _ *int, failure string, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status == nil {
		e.status, e.msg = map[string]dom.Status{}, map[string]string{}
	}
	e.status[step.Node], e.msg[step.Node] = status, failure
	return nil
}

func (e *endings) of(node string) (dom.Status, string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.status[node]
	return s, e.msg[node], ok
}

func oneStep() wf.Workflow {
	return wf.Workflow{Slug: "interrupted", Nodes: []wf.Node{{ID: "long", Run: "sleep 600"}}}
}

// D3: a step succeeded only if its executor said so. Before, the absence of a
// failure was read as success, and a step that reported nothing at all was
// recorded as having done its work.
func TestAStepWithNoVerdictIsNotASuccess(t *testing.T) {
	rows := &endings{spyPersister: &spyPersister{}}
	err := app.Runner{
		RunID: uuid.New(), Persist: rows, Report: &coletor{}, Processo: &mute{},
	}.Run(context.Background(), oneStep())

	if err == nil {
		t.Fatal("a step whose executor gave no verdict made the run succeed")
	}
	status, msg, ok := rows.of("long")
	if !ok || status != dom.StatusFailed || !strings.Contains(msg, "without an outcome") {
		t.Errorf("the step ended %s %q (recorded %v); wanted failed, without an outcome", status, msg, ok)
	}
}

// #122: a step interrupted by its context ends CANCELED -- it did not fail, it
// was stopped -- and the row is written although the run's context is gone.
// Before, it was recorded as a success, or not at all.
func TestAnInterruptedStepEndsCanceledAndIsRecorded(t *testing.T) {
	rows := &endings{spyPersister: &spyPersister{}}
	exec := &mute{waitForCancel: true}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	err := app.Runner{
		RunID: uuid.New(), Persist: rows, Report: &coletor{}, Processo: exec, MaxAttempts: 3,
	}.Run(ctx, oneStep())

	if err == nil {
		t.Fatal("an interrupted step made the run succeed")
	}
	status, msg, ok := rows.of("long")
	if !ok {
		t.Fatal("the interrupted step's end was never recorded; its row stays running forever")
	}
	if status != dom.StatusCanceled || !strings.Contains(msg, "interrupted") {
		t.Errorf("the step ended %s %q; wanted canceled, interrupted", status, msg)
	}
	if n := exec.calls.Load(); n != 1 {
		t.Errorf("the interrupted step was attempted %d times; a cancellation is not retried", n)
	}
}
