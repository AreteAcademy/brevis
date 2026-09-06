package local

import (
	"context"
	"fmt"
	"sync"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// GoExecutor roda tasks Go registradas, dentro do proprio processo.
//
// It is section 14's executor: no container, no pod, no child process. That is
// exactly the gain — a simple Go task does not pay container startup, which was
// the measurement that motivated this project (a 38s cold start against 5s in
// the benchmark Brevis came out of).
//
// Unlike the ProcessExecutor, there is NO environment restriction: the code here
// was compiled into the binary, so there is no arbitrary-execution surface.
type GoExecutor struct {
	reg *execution.Registry

	mu      sync.Mutex
	rodando map[string]context.CancelFunc
}

func NewGoExecutor(reg *execution.Registry) *GoExecutor {
	return &GoExecutor{reg: reg, rodando: map[string]context.CancelFunc{}}
}

func (g *GoExecutor) Name() string { return "go" }

// Execute resolves the task in the registry and runs it in a goroutine,
// streaming the
// eventos.
func (g *GoExecutor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	task, ok := g.reg.Get(t.Action)
	if !ok {
		// Listing what exists saves a trip to the documentation, and exposes a
		// mistake
		// de digitacao de imediato.
		disponiveis := g.reg.Nomes()
		if len(disponiveis) == 0 {
			// An empty registry is the common case today: `docker.run` and
			// `kubernetes.run` are in the plan but do not exist yet. Saying
			// "available: []" makes it look like a typo in the name.
			return nil, fmt.Errorf("task %q is not registered: no action is registered "+
				"in this worker — use `run:` with a command, or register the action in "+
				"the binary", t.Action)
		}
		return nil, fmt.Errorf("task %q is not registered (available: %v)", t.Action, disponiveis)
	}

	ctx, cancel := context.WithCancel(ctx)
	if t.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
	}

	g.mu.Lock()
	g.rodando[t.ExecutionID] = cancel
	g.mu.Unlock()

	eventos := make(chan execution.Event, 64)

	go func() {
		defer close(eventos)
		defer func() {
			g.mu.Lock()
			delete(g.rodando, t.ExecutionID)
			g.mu.Unlock()
			cancel()
		}()

		eventos <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}

		// A task that panics must not take the orchestrator down with it:
		// it runs in the SAME process, unlike a pod. The panic becomes a failure
		// daquela task.
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("task %q entrou em panico: %v", t.Action, r)
				}
			}()
			err = task.Run(ctx, execution.Input{
				NodeID: t.NodeID,
				With:   t.With,
				Log: func(msg string) {
					// it does not block when nobody is reading: a noisy task
					// must not stall because of its consumer
					select {
					case eventos <- execution.Event{
						Kind: execution.EventLog, NodeID: t.NodeID,
						Stream: "stdout", Message: msg,
					}:
					default:
					}
				},
			})
		}()

		switch {
		case err == nil:
			eventos <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID}
		case ctx.Err() == context.DeadlineExceeded:
			eventos <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID, Err: err,
				Message: fmt.Sprintf("estourou o timeout de %s", t.Timeout),
			}
		default:
			eventos <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID, Err: err,
				Message: err.Error(),
			}
		}
	}()

	return eventos, nil
}

func (g *GoExecutor) Cancel(_ context.Context, execID string) error {
	g.mu.Lock()
	cancel, ok := g.rodando[execID]
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %q is not running", execID)
	}
	cancel()
	return nil
}
