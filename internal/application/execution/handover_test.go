package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// handingOver is a host: step that, when its run's context ends, says where it
// goes on running instead of stopping -- the remote executor on a shutdown.
type handingOver struct {
	mu        sync.Mutex
	cancelled []string
}

func (*handingOver) Name() string { return "handing-over" }

func (h *handingOver) Cancel(_ context.Context, execID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelled = append(h.cancelled, execID)
	return nil
}

func (h *handingOver) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	ch := make(chan execution.Event, 2)
	go func() {
		defer close(ch)
		ch <- execution.Event{Kind: execution.EventStarted}
		<-ctx.Done()
		raw, _ := json.Marshal(execution.Handover{
			Instance: "http://agent-0:9443", ExecutionID: t.ExecutionID, Attempt: t.Attempt, After: 41,
		})
		ch <- execution.Event{Kind: execution.EventHandover, NodeID: t.NodeID, Message: string(raw)}
	}()
	return ch, nil
}

// refusing is a persister that cannot record a handover -- a database gone
// at the worst moment.
type refusing struct{ *endings }

func (refusing) RecordHandover(context.Context, uuid.UUID, dom.StepKey, int, execution.Handover) error {
	return errors.New("connection reset by peer")
}

func handOverOneStep(t *testing.T, persist app.Persister, exec *handingOver) error {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(30*time.Millisecond, func() { cancel(execution.ErrShutdown) })
	return app.Runner{
		RunID: uuid.New(), Persist: persist, Report: &coletor{},
		Hosts: map[string]execution.Executor{"box": exec},
	}.Run(ctx, wf.Workflow{Slug: "daily", Nodes: []wf.Node{{ID: "long", Host: "box", Run: "sleep 600"}}})
}

// #125: a step handed over is NOT ended -- it is running, on its agent -- and
// its row says where, for the next scheduler. The run answers that it was
// interrupted, so it is requeued as on any shutdown (#122).
func TestAHandedOverStepStaysRunningAndSaysWhere(t *testing.T) {
	rows := &endings{spyPersister: &spyPersister{}}
	exec := &handingOver{}

	err := handOverOneStep(t, rows, exec)

	var interrupted app.ErrInterrupted
	if !errors.As(err, &interrupted) {
		t.Fatalf("Run returned %v; wanted ErrInterrupted", err)
	}
	if status, _, ended := rows.of("long"); ended {
		t.Errorf("the handed-over step was ended %s; it is still running on its agent", status)
	}
	h, ok := rows.handover(dom.Step("long"))
	if !ok || h.Instance != "http://agent-0:9443" || h.After != 41 || h.ExecutionID == "" {
		t.Errorf("handover recorded = %+v (%v); wanted the agent's instance, the id and line 41", h, ok)
	}
	if len(exec.cancelled) != 0 {
		t.Errorf("a step handed over was cancelled: %v", exec.cancelled)
	}
}

// And a handover nobody wrote down is not a handover. The next scheduler
// could not find the step and would start it again beside itself, so it is
// stopped instead -- #122's behaviour -- and ends canceled.
func TestAHandoverThatCannotBeRecordedStopsTheStep(t *testing.T) {
	rows := &endings{spyPersister: &spyPersister{}}
	exec := &handingOver{}

	if err := handOverOneStep(t, refusing{rows}, exec); err == nil {
		t.Fatal("the run succeeded")
	}
	exec.mu.Lock()
	cancelled := append([]string(nil), exec.cancelled...)
	exec.mu.Unlock()
	if len(cancelled) != 1 {
		t.Errorf("cancels = %v; an unrecorded handover has to stop the step", cancelled)
	}
	if status, _, ended := rows.of("long"); !ended || status != dom.StatusCanceled {
		t.Errorf("the step ended %s (recorded %v); wanted canceled", status, ended)
	}
}
