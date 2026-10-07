package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// Start hands over the work and returns the stream, with the instance that
	// took it when the agent advertises one.
	Start(ctx context.Context, req StartRequest) (Session, error)

	// Resume picks the same execution's stream back up after `after`, AT THE
	// INSTANCE THAT TOOK IT. It returns a GapError when its buffer no longer
	// reaches that far.
	Resume(ctx context.Context, instance, execID string, r Resume) (io.ReadCloser, error)

	// Cancel asks the host to stop the process, at that same instance. See
	// Executor.Cancel for what its error means, which is the part that matters.
	Cancel(ctx context.Context, instance, execID string) error
}

// Session is what Start answers: the stream, and where to come back to.
//
// `Instance` is empty for an agent that advertises nothing, which is one agent
// at one address and every deployment that exists today. The engine then keeps
// using the address it was configured with, and nothing about this type
// changes what happens. See HeaderInstance.
type Session struct {
	Instance string
	Stream   io.ReadCloser
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
	running map[string]*step
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

	session, err := e.Agent.Start(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("the agent on %s refused the step: %w", e.Host, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	if e.running == nil {
		e.running = map[string]*step{}
	}
	// The instance is recorded WITH the cancel function, in one place, because
	// the two are needed together and by different callers: pump resumes and
	// Cancel stops, and a step whose instance lived somewhere else would be one
	// refactor away from being cancelled at the wrong replica.
	e.running[t.ExecutionID] = &step{cancel: cancel, instance: session.Instance}
	e.mu.Unlock()

	events := make(chan execution.Event, 64)
	go e.pump(ctx, cancel, t, session, events)
	return events, nil
}

// step is what the executor keeps while one execution is in flight.
type step struct {
	cancel   context.CancelFunc
	instance string
}

// pump reads the stream, resumes it when it breaks, and closes `events` once.
func (e *Executor) pump(ctx context.Context, cancel context.CancelFunc,
	t execution.TaskExec, session Session, events chan<- execution.Event) {

	stream := session.Stream

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

		next, rerr := e.Agent.Resume(ctx, session.Instance, t.ExecutionID, Resume{
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
	s := e.running[execID]
	e.mu.Unlock()
	var instance string
	if s != nil {
		s.cancel()
		instance = s.instance
	}

	if err := e.Agent.Cancel(ctx, instance, execID); err != nil {
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

func (a HTTPAgent) Start(ctx context.Context, r StartRequest) (Session, error) {
	stream, instance, err := a.send(ctx, a.BaseURL, "/v1/exec", r)
	if err != nil {
		return Session{}, err
	}
	return Session{Instance: instance, Stream: stream}, nil
}

func (a HTTPAgent) Resume(ctx context.Context, instance, execID string, r Resume) (io.ReadCloser, error) {
	stream, _, err := a.send(ctx, a.At(instance), "/v1/exec/"+execID+"/resume", r)
	return stream, err
}

func (a HTTPAgent) Cancel(ctx context.Context, instance, execID string) error {
	body, _, err := a.send(ctx, a.At(instance), "/v1/exec/"+execID+"/cancel", struct {
		Protocol int `json:"protocol"`
	}{Protocol})
	if err != nil {
		return err
	}
	return body.Close()
}

// At is the address to use for an execution already under way.
//
// The instance when it is one the installation already trusts, and the
// configured address otherwise. Empty is not a degraded case: it is one agent
// at one address, which is every deployment that does not put a pool behind a
// Service.
//
// BOUNDED, because the advertisement makes this engine POST to an address the
// AGENT chose. The token is not the new exposure -- every agent already
// receives it on every request -- but an arbitrary outbound POST from the
// engine is a capability that did not exist before the mechanism.
//
// SCHEME AND PORT ARE NOT A BOUND, and a test is what said so:
// `http://attacker.example.com:9443` shares both with the configured address,
// so a rule checking only those allows every host on earth. It looks like a
// bound and is one comparison away from nothing.
//
// What separates a replica from a stranger is the DOMAIN. A pool differs in
// the first DNS label -- `brevis-agent-2` for `brevis-agent-0` -- and shares
// everything after it. So: same scheme, same port, same domain. No second
// configuration, because the bound is derived from the address an operator
// already wrote into BREVIS_HOSTS.
//
// An address with no domain to share -- a bare host, an IP -- allows only
// itself. There is no first label to vary, so there is no pool to allow.
//
// Exported for the test that pins all of it. A refused advertisement is
// IGNORED rather than fatal: the engine falls back to the address it has, and
// on a pool that surfaces as the agent's own "not running here" rather than as
// a silence.
func (a HTTPAgent) At(instance string) string {
	if instance == "" || !sameInstallation(a.BaseURL, instance) {
		return a.BaseURL
	}
	return instance
}

// sameInstallation reports whether an advertised address is one the configured
// address already vouches for.
func sameInstallation(configured, advertised string) bool {
	base, err := url.Parse(configured)
	if err != nil {
		return false
	}
	adv, err := url.Parse(advertised)
	if err != nil || adv.Host == "" {
		return false
	}
	// THE PORT IS NOT CHECKED, and leaving it out is the second correction
	// this rule needed. `Service port 80 -> targetPort 9443` is an ordinary
	// topology: the configured address is the Service's port and the
	// advertisement is the pod's, so a port equality would refuse a pool that
	// is correctly deployed. It would also have broken the in-process pool
	// test, where a balancer and two agents cannot share one port.
	//
	// What it would have bought: stopping a compromised agent pointing the
	// engine at another PORT on a host inside the same domain. That is a real
	// but much smaller step -- everything in that domain is something this
	// operator's cluster already runs, and the POST carries a token the other
	// service will reject. Refusing a legitimate deployment to narrow that is
	// the wrong trade.
	if base.Scheme != adv.Scheme {
		return false
	}

	// THE ADVERTISED HOST IS THE CONFIGURED ONE, OR A NAME INSIDE IT.
	//
	// A pool is configured with its SERVICE -- `brevis-agent.dados.svc` -- and
	// each replica advertises its POD -- `brevis-agent-2.brevis-agent.dados.svc`.
	// The pod's name is the Service's with a label in FRONT, which is what
	// Kubernetes gives a StatefulSet with a governing headless Service, and it
	// is the only shape in which a pool works at all: configuring one pod
	// instead sends every start to that pod and the others never see work.
	//
	// So the rule is containment and not similarity. `evil.example.com` is
	// neither; `redis.dados.svc` is a sibling of the Service and is NOT a name
	// inside it; `pod.notbrevis-agent.dados.svc` ends with the right characters
	// and not with the right LABEL, which is why the dot is part of the
	// comparison.
	baseHost, advHost := base.Hostname(), adv.Hostname()
	return advHost == baseHost || strings.HasSuffix(advHost, "."+baseHost)
}

func (a HTTPAgent) send(ctx context.Context, base, path string, payload any) (io.ReadCloser, string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(string(raw)))
	if err != nil {
		return nil, "", err
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
		return nil, "", err
	}
	// Read BEFORE the status checks: a refusal carries it too, and an agent
	// that refused is still the one holding whatever it already started.
	instance := resp.Header.Get(HeaderInstance)
	if resp.StatusCode == http.StatusGone {
		// The agent's ring has moved past what was asked for.
		var gap GapError
		_ = json.NewDecoder(resp.Body).Decode(&gap)
		_ = resp.Body.Close()
		return nil, instance, gap
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, instance, fmt.Errorf("the agent answered %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return resp.Body, instance, nil
}
