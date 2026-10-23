package execution_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/agent"
	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// following is a host executor a step was handed over on. Reattach answers
// with reattachErr, or follows the step to a clean exit; Execute always runs
// it fresh to a clean exit. What was asked of it is kept.
type following struct {
	reattachErr error

	mu         sync.Mutex
	reattached []execution.Handover
	executed   []string
	cancelled  []string
}

func (*following) Name() string { return "following" }

func (f *following) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return nil
}

func done() <-chan execution.Event {
	ch := make(chan execution.Event, 2)
	ch <- execution.Event{Kind: execution.EventStarted}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch
}

func (f *following) Execute(_ context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executed = append(f.executed, t.ExecutionID)
	return done(), nil
}

func (f *following) Reattach(_ context.Context, _ execution.TaskExec, h execution.Handover) (<-chan execution.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reattached = append(f.reattached, h)
	if f.reattachErr != nil {
		return nil, f.reattachErr
	}
	return done(), nil
}

// attempts is a persister that keeps every attempt's start and end, so a test
// can see which attempt a reattached step continued and whether it was
// restarted on the way.
type attempts struct {
	*spyPersister
	mu      sync.Mutex
	started []int
	ended   map[int]dom.Status
	msg     map[int]string
}

func (a *attempts) IniciarTask(_ context.Context, _ uuid.UUID, _ dom.StepKey, attempt int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, attempt)
	return nil
}

func (a *attempts) TerminarTask(_ context.Context, _ uuid.UUID, _ dom.StepKey, attempt int,
	status dom.Status, _ *int, failure string, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended == nil {
		a.ended, a.msg = map[int]dom.Status{}, map[int]string{}
	}
	a.ended[attempt], a.msg[attempt] = status, failure
	return nil
}

// resumeAfterHandover is the next scheduler's run: one host step, handed over
// at attempt 1 by the scheduler before.
func resumeAfterHandover(t *testing.T, exec *following) (*attempts, *coletor, error) {
	t.Helper()
	rows := &attempts{spyPersister: &spyPersister{}}
	report := &coletor{}
	h := execution.Handover{Instance: "http://agent-0:9443", ExecutionID: "daily:long:x:r0:a1", Attempt: 1, After: 7}
	err := app.Runner{
		RunID: uuid.New(), Persist: rows, Report: report, MaxAttempts: 3,
		History: &historico{handovers: map[dom.StepKey]execution.Handover{dom.Step("long"): h}},
		Hosts:   map[string]execution.Executor{"box": exec},
	}.Run(context.Background(), wf.Workflow{Slug: "daily", Nodes: []wf.Node{{ID: "long", Host: "box", Run: "sleep 600"}}})
	return rows, report, err
}

func said(c *coletor, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.eventos {
		if strings.Contains(e.Message, text) {
			return true
		}
	}
	return false
}

// #125: a handed-over step is FOLLOWED, not started: the same execution, as
// the same attempt -- its row never restarted -- ending as it ends, with a line
// in the log saying what happened.
func TestAHandedOverStepIsFollowedNotStarted(t *testing.T) {
	exec := &following{}
	rows, report, err := resumeAfterHandover(t, exec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(exec.reattached) != 1 || len(exec.executed) != 0 {
		t.Errorf("reattached %d, executed %v; wanted the step followed once and never started",
			len(exec.reattached), exec.executed)
	}
	if len(rows.started) != 0 {
		t.Errorf("the attempt was started again (%v); a followed step keeps its row", rows.started)
	}
	if rows.ended[1] != dom.StatusSuccess {
		t.Errorf("attempt 1 ended %v; the followed attempt is the handed one", rows.ended)
	}
	if !said(report, "after a scheduler restart") {
		t.Error("the log does not say the step was followed after a restart")
	}
}

// The agent no longer knows it: nothing runs under that id, so the step starts
// again -- the re-run decided with #125's map.
func TestAStepTheAgentLostStartsAgain(t *testing.T) {
	exec := &following{reattachErr: fmt.Errorf("refused: %w", execution.ErrUnknownExecution)}
	rows, _, err := resumeAfterHandover(t, exec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(exec.executed) != 1 || len(exec.cancelled) != 0 {
		t.Errorf("executed %v, cancelled %v; wanted one fresh start and nothing stopped", exec.executed, exec.cancelled)
	}
	if rows.ended[1] != dom.StatusSuccess {
		t.Errorf("attempts ended %v; wanted attempt 1 run again and succeeding", rows.ended)
	}
}

// Known but past following: it may still be running, so it is stopped, and the
// attempt fails -- the ordinary retry then runs it under a new id.
func TestAStepThatCannotBeFollowedIsStoppedAndRetried(t *testing.T) {
	exec := &following{reattachErr: fmt.Errorf("gap: %w", execution.ErrLostTrack)}
	rows, _, err := resumeAfterHandover(t, exec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(exec.cancelled) != 1 || exec.cancelled[0] != "daily:long:x:r0:a1" {
		t.Errorf("cancelled %v; wanted the handed-over execution stopped", exec.cancelled)
	}
	if rows.ended[1] != dom.StatusFailed || !strings.Contains(rows.msg[1], "could not be followed") {
		t.Errorf("attempt 1 ended %v %q; wanted failed, saying it could not be followed", rows.ended[1], rows.msg[1])
	}
	if rows.ended[2] != dom.StatusSuccess || len(exec.executed) != 1 {
		t.Errorf("attempt 2 ended %v, executed %v; wanted the retry to run it fresh", rows.ended[2], exec.executed)
	}
}

// The agent cannot be reached: nothing is known, nothing is started under the
// old id, and the attempt fails as any start that cannot reach its agent.
func TestAnAgentThatCannotBeReachedFailsTheAttempt(t *testing.T) {
	exec := &following{reattachErr: errors.New("dial tcp 10.0.3.7:9443: connection refused")}
	rows, _, err := resumeAfterHandover(t, exec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rows.ended[1] != dom.StatusFailed || len(exec.cancelled) != 0 {
		t.Errorf("attempt 1 ended %v, cancelled %v; wanted failed, nothing stopped", rows.ended[1], exec.cancelled)
	}
	if rows.ended[2] != dom.StatusSuccess {
		t.Errorf("attempt 2 ended %v; wanted the retry", rows.ended[2])
	}
}

// memory is one run's rows, shared by the scheduler that stops and the one
// that comes back: what the first hands over, the second reads.
type memory struct{ *spyPersister }

func (memory) StepHasSucceeded(context.Context, string, string, uuid.UUID) (bool, error) {
	return false, nil
}

func (m memory) AlreadySucceeded(context.Context, uuid.UUID) (map[dom.StepKey]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[dom.StepKey]bool{}
	for k, s := range m.ended {
		out[k] = s == dom.StatusSuccess
	}
	return out, nil
}

func (m memory) Handovers(context.Context, uuid.UUID) (map[dom.StepKey]execution.Handover, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[dom.StepKey]execution.Handover{}
	for k, h := range m.handovers {
		if _, ended := m.ended[k]; !ended {
			out[k] = h
		}
	}
	return out, nil
}

// #125 end to end, on a real agent: a scheduler stops mid-step and hands it
// over; the next one, for the same run, follows it to its end. The step runs
// ONCE, ends as it ended, and what it published arrives.
func TestAStepOutlivesItsSchedulerAndIsFollowedToItsEnd(t *testing.T) {
	// The step writes nine lines: started, "first" -- the last one the
	// stopping scheduler reads -- four after its sleep, "last", the published
	// context, its exit.
	for _, c := range []struct {
		name      string
		ring      int
		wantFirst bool // the lines before the handover are in the log
		lost      bool // not even the last line read is left: D5's gap case
	}{
		{"the ring still has its start", 0, true, false},
		{"the ring lost its start, not the last line read", 7, false, false},
		{"the ring lost the last line read too", 3, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}, RingSize: c.ring})
			srv := httptest.NewServer(a.Handler(slog.New(slog.NewTextHandler(io.Discard, nil))))
			t.Cleanup(srv.Close)
			hosts := map[string]execution.Executor{"box": &remote.Executor{
				Host: "box", Agent: remote.HTTPAgent{BaseURL: srv.URL}, LeaseLimit: 10 * time.Second, HandOver: true,
			}}
			rows := memory{&spyPersister{}}
			runID := uuid.New()
			w := wf.Workflow{Slug: "daily", Nodes: []wf.Node{{ID: "long", Host: "box",
				Run: `echo first; sleep 1; for i in 1 2 3 4; do echo late$i; done; ` +
					`echo '{"k":1}' > "$BREVIS_OUTPUT"; echo last`}}}

			// The scheduler that stops: a SIGTERM while the step sleeps.
			ctx, sigterm := context.WithCancelCause(context.Background())
			time.AfterFunc(300*time.Millisecond, func() { sigterm(execution.ErrShutdown) })
			err := app.Runner{RunID: runID, Persist: rows, History: rows, Report: &coletor{}, Hosts: hosts}.Run(ctx, w)
			var interrupted app.ErrInterrupted
			if !errors.As(err, &interrupted) {
				t.Fatalf("the first scheduler's run returned %v; wanted ErrInterrupted", err)
			}
			if _, ok := rows.handover(dom.Step("long")); !ok {
				t.Fatal("nothing was handed over")
			}

			// The scheduler that comes back, for the same run -- after the
			// step has ENDED on its agent, so what is followed is the outcome
			// the agent kept (#128), and a small ring has lost the start.
			time.Sleep(1500 * time.Millisecond)
			report := &coletor{}
			err = app.Runner{RunID: runID, Persist: rows, History: rows, Report: report, Hosts: hosts}.
				Run(context.Background(), w)
			if c.lost {
				// Stopped and failed, for the ordinary retry to run under a
				// new id -- approved as the cost of this case.
				if err == nil || !strings.Contains(err.Error(), "could not be followed") {
					t.Errorf("the next scheduler's run returned %v; wanted the attempt failed, not followed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the next scheduler's run: %v", err)
			}

			rows.mu.Lock()
			defer rows.mu.Unlock()
			if rows.ended[dom.Step("long")] != dom.StatusSuccess {
				t.Errorf("the followed step ended %v", rows.ended[dom.Step("long")])
			}
			if !strings.Contains(rows.log, "last") || strings.Contains(rows.log, "first") != c.wantFirst {
				t.Errorf("the step's log %q; wanted its end, and its start only while the ring had it", rows.log)
			}
			if !strings.Contains(string(rows.ctxOut), `"k":1`) {
				t.Errorf("the context the step published did not arrive: %s", rows.ctxOut)
			}
			if !said(report, "after a scheduler restart") {
				t.Error("the log does not say the step was followed after a restart")
			}
		})
	}
}
