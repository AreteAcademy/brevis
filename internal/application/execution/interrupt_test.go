package execution_test

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/AreteAcademy/brevis/internal/execution/remote"
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

// lastWord succeeds every step -- and cancels the run as the first one ends,
// which is a SIGTERM landing between two levels.
type lastWord struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (*lastWord) Name() string                         { return "last-word" }
func (*lastWord) Cancel(context.Context, string) error { return nil }

func (l *lastWord) Execute(_ context.Context, _ execution.TaskExec) (<-chan execution.Event, error) {
	l.calls.Add(1)
	l.cancel()
	ch := make(chan execution.Event, 2)
	ch <- execution.Event{Kind: execution.EventStarted}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch, nil
}

func twoLevels() wf.Workflow {
	return wf.Workflow{Slug: "interrupted", Nodes: []wf.Node{
		{ID: "first", Run: "true"}, {ID: "second", Run: "true"},
	}, Edges: []wf.Edge{{From: "first", To: "second"}}}
}

// #122's first point: a cancel between levels stopped the loop and returned
// the first FAILURE -- nil, since nothing had failed -- and the dispatcher
// marked a run cut in half as a success.
func TestARunCutBetweenLevelsSaysItWasInterrupted(t *testing.T) {
	rows := &endings{spyPersister: &spyPersister{}}
	ctx, cancel := context.WithCancel(context.Background())
	exec := &lastWord{cancel: cancel}

	err := app.Runner{
		RunID: uuid.New(), Persist: rows, Report: &coletor{}, Processo: exec,
	}.Run(ctx, twoLevels())

	var interrupted app.ErrInterrupted
	if !errors.As(err, &interrupted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v; wanted ErrInterrupted wrapping context.Canceled", err)
	}
	if n := exec.calls.Load(); n != 1 {
		t.Errorf("%d steps ran; the second level starts after the cancel and must not", n)
	}
	if _, _, ok := rows.of("second"); ok {
		t.Error("the second level has a row; it was never started, nor ruled out")
	}
}

// And inside a level: the interrupted step's own error is not the run's
// answer. The run was stopped, and that is what it says.
func TestARunCutInsideALevelSaysItWasInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	err := app.Runner{
		RunID: uuid.New(), Persist: &endings{spyPersister: &spyPersister{}}, Report: &coletor{},
		Processo: &mute{waitForCancel: true},
	}.Run(ctx, oneStep())

	var interrupted app.ErrInterrupted
	if !errors.As(err, &interrupted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v; wanted ErrInterrupted wrapping context.Canceled", err)
	}
}

// timedOut fails its step with an error that wraps a context's -- an HTTP call
// to an agent timing out -- while the run itself goes on.
type timedOut struct{}

func (timedOut) Name() string                         { return "timed-out" }
func (timedOut) Cancel(context.Context, string) error { return nil }
func (timedOut) Execute(context.Context, execution.TaskExec) (<-chan execution.Event, error) {
	return nil, fmt.Errorf("the agent on box refused the step: %w", context.DeadlineExceeded)
}

// A failure that merely mentions a context is a failure. Only the run's own
// context ending is an interruption -- otherwise a broken agent would be
// requeued forever without spending an attempt.
func TestAFailureMentioningAContextIsNotAnInterruption(t *testing.T) {
	err := app.Runner{
		RunID: uuid.New(), Persist: &endings{spyPersister: &spyPersister{}}, Report: &coletor{},
		Processo: timedOut{},
	}.Run(context.Background(), oneStep())

	var interrupted app.ErrInterrupted
	if err == nil || errors.As(err, &interrupted) {
		t.Fatalf("Run returned %v; wanted an ordinary failure", err)
	}
}

// holdingAgent takes a `host:` step at one instance and keeps it running until
// told to stop -- the agent of #122's reproduction, with `sleep 600` on it.
type holdingAgent struct {
	mu      sync.Mutex
	cancels []string
}

type untilDone struct{ ctx context.Context }

func (u untilDone) Read([]byte) (int, error) { <-u.ctx.Done(); return 0, io.EOF }
func (untilDone) Close() error               { return nil }

func (*holdingAgent) Start(ctx context.Context, _ remote.StartRequest) (remote.Session, error) {
	return remote.Session{Instance: "http://agent-0:9443", Stream: untilDone{ctx: ctx}}, nil
}

func (*holdingAgent) Resume(ctx context.Context, _, _ string, _ remote.Resume) (io.ReadCloser, error) {
	return untilDone{ctx: ctx}, nil
}

func (h *holdingAgent) Cancel(_ context.Context, instance, execID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels = append(h.cancels, instance+"/"+execID)
	return nil
}

// #122's second point, through the whole path a shutdown takes: the runner,
// the real remote executor, an agent. Cancelling the run's context stops the
// process on the agent -- once, at the instance that took it -- records the
// step as canceled, and the run answers that it was interrupted. On 0.35.0 the
// agent's `sleep 600` was still there after the scheduler came back.
func TestAShutdownStopsAHostStepOnItsAgent(t *testing.T) {
	agent := &holdingAgent{}
	rows := &endings{spyPersister: &spyPersister{}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	err := app.Runner{
		RunID: uuid.New(), Persist: rows, Report: &coletor{},
		Hosts: map[string]execution.Executor{
			"box": &remote.Executor{Agent: agent, Host: "box", LeaseLimit: time.Minute},
		},
	}.Run(ctx, wf.Workflow{Slug: "interrupted", Nodes: []wf.Node{
		{ID: "long", Host: "box", Run: "sleep 600"},
	}})

	var interrupted app.ErrInterrupted
	if !errors.As(err, &interrupted) {
		t.Fatalf("Run returned %v; wanted ErrInterrupted", err)
	}
	if status, _, ok := rows.of("long"); !ok || status != dom.StatusCanceled {
		t.Errorf("the host step ended %s (recorded %v); wanted canceled", status, ok)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.cancels) != 1 || !strings.HasPrefix(agent.cancels[0], "http://agent-0:9443/") {
		t.Errorf("cancels = %v; wanted one, at the instance that took the step", agent.cancels)
	}
}
