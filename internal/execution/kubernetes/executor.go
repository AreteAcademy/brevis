package kubernetes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// API is what the executor needs from the server. An interface in the consumer:
// it is what makes it possible to test the pod's whole lifecycle against a fake
// server.
type API interface {
	CreatePod(ctx context.Context, p Pod) (Pod, error)
	LerPod(ctx context.Context, name string) (Pod, error)
	Logs(ctx context.Context, name string, follow1 bool) (io.ReadCloser, error)
	DeletePod(ctx context.Context, name string) error
}

// Executor runs each step as a pod.
//
// The cycle is always the same: create the pod, wait for it to leave Pending,
// follow the log while it runs, read the exit code and delete it. No state lives
// here beyond the in-flight pods -- if the process restarts, the pods keep
// running and the dispatcher finds them again by their deterministic name.
type Executor struct {
	api  API
	opts Options

	// Status polling interval. Polling rather than watching is deliberate: a
	// watch needs reconnection, resync and handling of missed events to gain
	// seconds on a task that lasts minutes.
	Interval time.Duration

	mu       sync.Mutex
	inFlight map[string]string // execID -> pod name
}

func NewExecutor(api API, o Options) *Executor {
	return &Executor{
		api: api, opts: o.withDefaults(),
		Interval: time.Second,
		inFlight: map[string]string{},
	}
}

func (e *Executor) Name() string { return "kubernetes" }

// Execute creates the pod and returns the event channel. The channel closes when
// the pod finishes -- the same shape as the local executor, so the runner cannot
// tell them apart.
func (e *Executor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	spec, err := BuildPod(t, e.opts)
	if err != nil {
		return nil, err
	}

	created, err := e.api.CreatePod(ctx, spec)
	if err != nil {
		// AlreadyExists is not an error: the name is deterministic per attempt,
		// so this means an earlier run created the pod and died before
		// recording it. Adopting the existing pod avoids running the same dbt
		// twice in parallel.
		if !strings.Contains(err.Error(), "already exists") {
			return nil, err
		}
		created = spec
	}
	name := created.Metadata.Name

	e.mu.Lock()
	e.inFlight[t.ExecutionID] = name
	e.mu.Unlock()

	events := make(chan execution.Event, 64)
	go func() {
		defer close(events)
		defer func() {
			e.mu.Lock()
			delete(e.inFlight, t.ExecutionID)
			e.mu.Unlock()
		}()
		e.follow(ctx, name, t, events)
	}()
	return events, nil
}

func (e *Executor) follow(ctx context.Context, name string, t execution.TaskExec, events chan<- execution.Event) {
	events <- execution.Event{
		Kind: execution.EventStarted, NodeID: t.NodeID,
		Message: fmt.Sprintf("pod %s (%s)", name, t.Image),
	}

	pod, err := e.esperarSair(ctx, name, t, events)
	if err != nil {
		events <- execution.Event{
			Kind: execution.EventFailed, NodeID: t.NodeID,
			Message: err.Error(), Err: err,
		}
		e.cleanUp(name, false)
		return
	}

	// The log is drained to the end BEFORE reporting the outcome: closing the
	// channel with lines still buffered would lose precisely the last ones,
	// which are the ones that explain the failure.
	e.drainLogs(ctx, name, t, events)

	// What the step published, before the outcome is reported -- the runner
	// keys it by node and the next step needs it either way.
	if published := pod.PublishedContext(); published != "" {
		events <- execution.Event{
			Kind: execution.EventContext, NodeID: t.NodeID, Message: published,
		}
	}

	codigo, finished := pod.Output()
	if pod.Fase() == "Succeeded" {
		events <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID, ExitCode: codigo}
		e.cleanUp(name, true)
		return
	}

	msg := fmt.Sprintf("pod %s terminou em %s", name, pod.Fase())
	if finished {
		msg = fmt.Sprintf("exited with code %d", codigo)
	}
	if pod.Reason() != "" {
		// DeadlineExceeded, OOMKilled, Evicted: the difference between "the code
		// failed" and "the cluster killed the process".
		msg += " (" + pod.Reason() + ")"
	}
	events <- execution.Event{
		Kind: execution.EventFailed, NodeID: t.NodeID,
		ExitCode: codigo, Message: msg, Err: errors.New(msg),
	}
	e.cleanUp(name, false)
}

// esperarSair polls until the pod finishes, reporting why it is waiting.
func (e *Executor) esperarSair(ctx context.Context, name string, t execution.TaskExec,
	events chan<- execution.Event) (Pod, error) {

	tick := time.NewTicker(e.Interval)
	defer tick.Stop()

	// The log follower writes to the SAME channel the caller closes on the way
	// out. Without waiting for it, `close(eventos)` could fire with a send in
	// flight -- `send on closed channel`, which brings down the whole process
	// and not just the run. The -race detector found this the first time the
	// root module was tested in CI.
	//
	// Cancelling before waiting is what bounds the wait: the log response's
	// body closes with the context, so the follower exits. Losing the rest of
	// the live follow costs nothing -- drainLogs reads the whole log right
	// afterwards, which is how the last lines already arrived.
	ctxLogs, pararLogs := context.WithCancel(ctx)
	var seguidores sync.WaitGroup
	defer func() {
		pararLogs()
		seguidores.Wait()
	}()

	var lastReason string
	seguindo := false
	started := time.Now()

	for {
		pod, err := e.api.LerPod(ctx, name)
		if err != nil {
			return Pod{}, fmt.Errorf("lendo pod %s: %w", name, err)
		}
		if pod.Finished() {
			return pod, nil
		}

		// A pod stuck in ImagePullBackOff or CreateContainerConfigError produces
		// no log at all: without reporting the reason, the step would look
		// jammed until the timeout, with not a line explaining it.
		if reason := pod.WaitReason(); reason != "" && reason != lastReason {
			lastReason = reason
			events <- execution.Event{
				Kind: execution.EventLog, NodeID: t.NodeID, Stream: "stderr",
				Message: "pod aguardando: " + reason,
			}
		}

		// As soon as the container runs, the log is followed in parallel -- the
		// operator sees dbt's output live rather than only at the end.
		if !seguindo && pod.Fase() == "Running" {
			seguindo = true
			seguidores.Add(1)
			go func() {
				defer seguidores.Done()
				e.followLogs(ctxLogs, name, t, events)
			}()
		}

		// A pod that never leaves Pending is not an error to Kubernetes: it
		// waits forever for a node it fits on. Without this cut-off the step
		// waits with it -- no failure and no retry -- which is how a CPU request
		// larger than the pool's free capacity jammed an entire run in dev.
		if !seguindo && time.Since(started) > e.opts.EsperaParaIniciar {
			reason := pod.WaitReason()
			if reason == "" {
				reason = "fase " + pod.Fase()
			}
			if scheduling := e.whyNotScheduled(ctx, name); scheduling != "" {
				reason = scheduling
			}
			return Pod{}, fmt.Errorf("pod %s did not start within %s: %s",
				name, e.opts.EsperaParaIniciar, reason)
		}

		select {
		case <-ctx.Done():
			return Pod{}, ctx.Err()
		case <-tick.C:
		}
	}
}

// whyNotScheduled reads the PodScheduled condition, which is where the
// scheduler explains "Insufficient cpu" or "didn't match node affinity".
// Without it the message would say only "Pending", which helps nobody.
func (e *Executor) whyNotScheduled(ctx context.Context, name string) string {
	pod, err := e.api.LerPod(ctx, name)
	if err != nil || pod.Status == nil {
		return ""
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status != "True" && c.Message != "" {
			return c.Reason + ": " + c.Message
		}
	}
	return ""
}

// followLogs follows the output while the container lives.
func (e *Executor) followLogs(ctx context.Context, name string, t execution.TaskExec, events chan<- execution.Event) {
	body, err := e.api.Logs(ctx, name, true)
	if err != nil {
		return // the log may not be ready; drainLogs still reads it at the end
	}
	defer func() { _ = body.Close() }()
	copyOut(body, t.NodeID, events)
}

// drainLogs reads the complete output once the pod has finished.
//
// Without following: the container is over, and `follow` on a closed output only
// returns the same content. The repeated lines from the stretch already streamed
// are the price of not losing the end -- and losing the end is what stops anyone
// understanding the failure.
func (e *Executor) drainLogs(ctx context.Context, name string, t execution.TaskExec, events chan<- execution.Event) {
	body, err := e.api.Logs(ctx, name, false)
	if err != nil {
		events <- execution.Event{
			Kind: execution.EventLog, NodeID: t.NodeID, Stream: "stderr",
			Message: "could not read the pod's log: " + err.Error(),
		}
		return
	}
	defer func() { _ = body.Close() }()
	copyOut(body, t.NodeID, events)
}

func copyOut(r io.Reader, nodeID string, events chan<- execution.Event) {
	s := bufio.NewScanner(r)
	// A dbt line carrying SQL can exceed 64 KB, the Scanner's default limit --
	// and a Scanner that overflows stops reading in silence.
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		// The pod's log arrives on a single stream: Kubernetes does not separate
		// stdout from stderr. Marking everything as stdout is a smaller lie than
		// the opposite, but the origin information simply does not exist here.
		events <- execution.Event{
			Kind: execution.EventLog, NodeID: nodeID,
			Stream: "stdout", Message: s.Text(),
		}
	}
}

// limpar deletes the pod, honouring the option to keep the failed ones.
func (e *Executor) cleanUp(name string, succeeded bool) {
	if !succeeded && e.opts.KeepFailedPod {
		return
	}
	// A context of its own: the run's may already be cancelled (the
	// cancellation is what brought us here), and deleting is precisely what
	// must not be skipped -- an orphaned pod consumes namespace quota
	// forever.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = e.api.DeletePod(ctx, name)
}

// Cancel deletes the pod of the run in flight.
func (e *Executor) Cancel(ctx context.Context, execID string) error {
	e.mu.Lock()
	name, ok := e.inFlight[execID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %q is not running", execID)
	}
	return e.api.DeletePod(ctx, name)
}
