package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// Agent is the host's side, as this executor needs it.
//
// An interface rather than an HTTP client directly, and that is not ceremony:
// the contract has to be settled by tests before a second program exists to keep
// in sync with it. An agent written first would become the specification by
// accident, and the specification would then be whatever that program happened
// to do.
type Agent interface {
	// Start hands over the work and returns the stream of NDJSON lines.
	Start(ctx context.Context, req StartRequest) (io.ReadCloser, error)

	// Resume picks the same execution's stream back up after `after`.
	// It returns a GapError when its buffer no longer reaches that far.
	Resume(ctx context.Context, execID string, r Resume) (io.ReadCloser, error)

	// Cancel asks the host to stop the process. See Executor.Cancel for what
	// its error means, which is the part that matters.
	Cancel(ctx context.Context, execID string) error
}

// Executor runs steps on one host.
type Executor struct {
	Agent Agent
	Host  string

	// LeaseLimit is how long the stream may go without any line -- including
	// the agent's periodic `alive` -- before the step is failed. Zero takes
	// DefaultLeaseLimit.
	LeaseLimit time.Duration

	// Retries is how many times a dropped connection is resumed before the step
	// is failed. Zero takes defaultRetries.
	//
	// It is bounded rather than endless because an agent that drops the
	// connection every second is not a network blip, and retrying forever turns
	// a broken host into a step that never ends.
	Retries int

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

const defaultRetries = 5

func (e *Executor) Name() string {
	if e.Host == "" {
		return "remote"
	}
	return "remote:" + e.Host
}

func (e *Executor) leaseLimit() time.Duration {
	if e.LeaseLimit > 0 {
		return e.LeaseLimit
	}
	return DefaultLeaseLimit
}

// Execute hands the step to the agent and turns its stream into events.
func (e *Executor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	if t.Command == "" {
		return nil, fmt.Errorf("task %q has no command, and a remote host runs nothing else: "+
			"`action:` resolves in this process's own registry and cannot cross a machine",
			t.NodeID)
	}
	if t.Image != "" {
		return nil, fmt.Errorf("task %q names both a host and an image, and they are "+
			"alternatives: `image:` builds a pod, `host:` sends the command to a machine "+
			"that already has what it needs", t.NodeID)
	}

	req := StartRequest{
		Protocol:    Protocol,
		ExecutionID: t.ExecutionID,
		NodeID:      t.NodeID,
		Workflow:    t.Workflow,
		RunID:       t.RunID,
		Attempt:     t.Attempt,
		Command:     t.Command,
		WorkDir:     t.WorkDir,
		Env:         t.Env,
		// UNRESOLVED, and this is decision 3. See StartRequest.Secrets.
		Secrets:        t.Secrets,
		TimeoutSeconds: int(t.Timeout.Seconds()),
	}

	stream, err := e.Agent.Start(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("the agent on %s refused the step: %w", e.Host, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	if e.running == nil {
		e.running = map[string]context.CancelFunc{}
	}
	e.running[t.ExecutionID] = cancel
	e.mu.Unlock()

	events := make(chan execution.Event, 64)
	go e.pump(ctx, cancel, t, stream, events)
	return events, nil
}

// pump reads the stream, resumes it when it breaks, and closes `events` once.
func (e *Executor) pump(ctx context.Context, cancel context.CancelFunc,
	t execution.TaskExec, stream io.ReadCloser, events chan<- execution.Event) {

	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.running, t.ExecutionID)
		e.mu.Unlock()
		close(events)
	}()

	retries := e.Retries
	if retries <= 0 {
		retries = defaultRetries
	}

	var seen int64
	for attempt := 0; ; attempt++ {
		last, finished, err := e.drain(ctx, t, stream, events, seen)
		_ = stream.Close()
		seen = last

		if finished {
			return
		}
		if ctx.Err() != nil {
			// Cancelled. The step's outcome was already reported by whoever
			// cancelled it; saying anything here would be a second verdict.
			return
		}

		// No gap check here, deliberately: `drain` reports what the CONNECTION
		// did, and a gap is what the AGENT says when asked to resume. The check
		// belongs below, where Resume answers, and having one here as well was
		// a branch nothing could reach -- proved by deleting it and watching
		// every test stay green.
		if attempt >= retries {
			events <- failure(t.NodeID, fmt.Sprintf(
				"the stream from %s broke %d times and the step was given up on. "+
					"The process may still be running on the host: %v",
				e.Host, attempt+1, err))
			return
		}

		next, rerr := e.Agent.Resume(ctx, t.ExecutionID, Resume{
			Protocol: Protocol, After: seen, Reason: reasonOf(err),
		})
		if rerr != nil {
			// A GapError arrives here like any other refusal and needs no
			// branch of its own: it explains itself, and %v carries that whole
			// explanation through. There WAS a special case for it, removed
			// after a mutation showed it changed nothing -- two branches
			// producing the same message is the shape that drifts apart.
			events <- failure(t.NodeID, fmt.Sprintf(
				"the stream from %s broke and could not be resumed after line %d: %v",
				e.Host, seen, rerr))
			return
		}
		stream = next
	}
}

// drain reads one connection to exhaustion.
//
// It returns the last sequence it accepted, whether the step FINISHED (an exit
// or a refusal, not merely a closed connection), and why it stopped.
func (e *Executor) drain(ctx context.Context, t execution.TaskExec, stream io.Reader,
	events chan<- execution.Event, seen int64) (int64, bool, error) {

	lines := make(chan Line)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		s := bufio.NewScanner(stream)
		// The same ceiling the other two executors raised, and for the same
		// reason: a dbt line carrying SQL exceeds the scanner's 64 KB default,
		// and a Scanner that overflows stops reading in silence.
		s.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for s.Scan() {
			raw := strings.TrimSpace(s.Text())
			if raw == "" {
				continue
			}
			l, err := Decode([]byte(raw))
			if err != nil {
				errs <- err
				return
			}
			select {
			case lines <- l:
			case <-ctx.Done():
				return
			}
		}
		errs <- s.Err()
	}()

	// The lease. Any line renews it, not just `alive`: a step producing output
	// is plainly alive, and requiring the heartbeat on top would fail a healthy
	// step whose agent was busy writing its log.
	limit := e.leaseLimit()
	timer := time.NewTimer(limit)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return seen, false, ctx.Err()

		case <-timer.C:
			return seen, true, e.leaseExpired(t, events, limit)

		case err := <-errs:
			if err == nil {
				err = io.EOF
			}
			return seen, false, err

		case l, ok := <-lines:
			if !ok {
				return seen, false, io.EOF
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(limit)

			// Already seen. A resumed stream may overlap, and replaying a line
			// is not free: a counter metric inside it would be added twice.
			if l.Seq <= seen {
				continue
			}
			seen = l.Seq

			if done := e.emit(t, l, events); done {
				return seen, true, nil
			}
		}
	}
}

// emit turns one line into an event, and says whether the step is over.
func (e *Executor) emit(t execution.TaskExec, l Line, events chan<- execution.Event) bool {
	switch l.Kind {
	case KindStarted:
		events <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}

	case KindLog:
		stream := l.Stream
		if stream == "" {
			stream = "stdout"
		}
		events <- execution.Event{
			Kind: execution.EventLog, NodeID: t.NodeID,
			Stream: stream, Message: l.Message,
		}

	case KindAlive:
		// Nothing. The renewal already happened in drain, where ANY line resets
		// the lease -- a heartbeat is not something a person needs in a log.
		//
		// An empty case rather than no case: falling through would behave
		// identically, so this line is documentation and no mutation can kill
		// it. It is here because "alive is deliberately silent" is worth a
		// reader knowing, and the alternative is finding out by grepping for a
		// kind that appears nowhere.

	case KindFailed:
		events <- failure(t.NodeID, fmt.Sprintf("the agent on %s could not run the step: %s",
			e.Host, l.Message))
		return true

	case KindExit:
		if l.Code == 0 {
			events <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID}
		} else {
			events <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID,
				ExitCode: l.Code,
				Message:  fmt.Sprintf("the step exited with code %d on %s", l.Code, e.Host),
			}
		}
		return true
	}
	return false
}

// leaseExpired fails a step whose agent stopped reporting.
//
// The message names what is NOT known, because that is the actionable half: the
// engine gave up, and the process may well still be running on a host it cannot
// see. Saying "the step failed" alone would send somebody looking for a failure
// that may not exist.
func (e *Executor) leaseExpired(t execution.TaskExec, events chan<- execution.Event,
	limit time.Duration) error {

	msg := fmt.Sprintf("the agent on %s sent nothing for %s, so this step is given up on. "+
		"It renews a lease every %s while it holds a step, and a step that is merely "+
		"quiet still renews -- so silence this long means the agent stopped, not that "+
		"the step is slow. THE PROCESS MAY STILL BE RUNNING on the host",
		e.Host, limit, DefaultAliveEvery)
	events <- failure(t.NodeID, msg)
	return errors.New(msg)
}

func failure(nodeID, msg string) execution.Event {
	return execution.Event{
		Kind: execution.EventFailed, NodeID: nodeID,
		ExitCode: -1, Message: msg, Err: errors.New(msg),
	}
}

func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Cancel asks the host to stop the process, and is honest about what it
// achieved.
//
// Decision 2, and the only one of the four with no precedent in this codebase --
// nothing here has ever had to cancel something it did not spawn.
//
// Two things this does NOT hide:
//
//   - the local context is cancelled first, so the engine stops reading the
//     stream whatever the agent does. The step's own outcome is then decided by
//     the caller, not by this stream going quiet.
//   - if the agent is unreachable, the error says so plainly. The step is
//     cancelled HERE and may well still be running THERE, which is a real state
//     and worth naming rather than showing as a spinner that never resolves.
func (e *Executor) Cancel(ctx context.Context, execID string) error {
	e.mu.Lock()
	stop := e.running[execID]
	e.mu.Unlock()
	if stop != nil {
		stop()
	}

	if err := e.Agent.Cancel(ctx, execID); err != nil {
		return fmt.Errorf("the engine stopped following this step, but the agent on %s "+
			"could not be reached to stop it: %w. The process may still be running there",
			e.Host, err)
	}
	return nil
}

// HTTPAgent is the real agent, over HTTP.
//
// Deliberately thin: everything that decides behaviour is in Executor and is
// tested against a fake. This is transport.
type HTTPAgent struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (a HTTPAgent) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	// No overall timeout: a step legitimately runs for hours, and a client
	// timeout would cut it at an arbitrary point. What catches a dead agent is
	// the lease, which measures silence rather than duration.
	return &http.Client{}
}

func (a HTTPAgent) Start(ctx context.Context, r StartRequest) (io.ReadCloser, error) {
	return a.post(ctx, "/v1/exec", r)
}

func (a HTTPAgent) Resume(ctx context.Context, execID string, r Resume) (io.ReadCloser, error) {
	return a.post(ctx, "/v1/exec/"+execID+"/resume", r)
}

func (a HTTPAgent) Cancel(ctx context.Context, execID string) error {
	body, err := a.post(ctx, "/v1/exec/"+execID+"/cancel", struct {
		Protocol int `json:"protocol"`
	}{Protocol})
	if err != nil {
		return err
	}
	return body.Close()
}

func (a HTTPAgent) post(ctx context.Context, path string, payload any) (io.ReadCloser, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+path, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.Token != "" {
		// A shared token, and the gap is stated rather than discovered: no
		// revocation of one host without changing every host, and no per-host
		// identity in the audit trail. See docs/KUBERNETES.md's remote section.
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}

	resp, err := a.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusGone {
		// The agent's ring has moved past what was asked for.
		var gap GapError
		_ = json.NewDecoder(resp.Body).Decode(&gap)
		_ = resp.Body.Close()
		return nil, gap
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("the agent answered %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return resp.Body, nil
}
