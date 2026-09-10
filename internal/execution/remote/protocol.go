// Package remote runs a step on a host the engine does not manage.
//
// The third executor. `local` runs a process on this machine and `kubernetes`
// creates a pod; this one hands the work to an agent on a host somebody else
// administers -- an EC2 instance, a VM, a box in another cluster, a licensed
// tool that cannot be containerised, a GPU machine.
//
// # What made it possible
//
// Nothing in this package returns context to the engine, and that is deliberate.
// A pod publishes through the kubelet's termination message and a host outside
// the cluster has no equivalent, which was the blocker on this whole executor.
// It was answered elsewhere: a step publishes on stdout as a `@brevis:` marked
// line, on the pipe that already carries its phases. The runner reads it out of
// the log stream, so this executor only has to stream logs faithfully and the
// return path follows.
//
// # The four decisions
//
// Each is a place where a thin version would ship and then hurt. Three of them
// resolve to something this codebase already does; the reasoning is in the
// commit that introduced this package.
//
//  1. STREAMING. NDJSON with a sequence number per line, and a bounded ring on
//     the agent so a dropped connection resumes rather than failing a
//     forty-minute load. The sequence number is not tidiness: replayed log
//     lines, phases, published context and gauges are all idempotent, but a
//     COUNTER metric is not -- the engine's registry does `Add`, so a replayed
//     line adds twice and reads high while nothing fails.
//
//  2. CANCEL. See Cancel.
//
//  3. SECRETS. The agent resolves them. The engine has never transported a
//     secret value -- kubernetes emits a secretKeyRef for the kubelet, local
//     passes along variables already in its own environment -- and this
//     executor does not become the first. See the `Secrets` field of the
//     request below.
//
//  4. LIVENESS. An agent that died is indistinguishable from one running a long
//     step. The stream carries a periodic `alive` line, which is the lease: the
//     same claim-renew-sweep shape as queue.Recover and alerts.Outbox.Recover,
//     riding a channel that already exists rather than opening a second one.
package remote

import (
	"encoding/json"
	"fmt"
	"time"
)

// Protocol is the wire version. It goes in every request, and an agent that
// does not recognise it must refuse rather than guess: a silent mismatch
// between two programs on different release cycles is the failure this number
// exists to make loud.
const Protocol = 1

// StartRequest is what the engine asks the agent to run.
//
// It is TaskExec's fields, minus everything that means nothing on a host the
// engine does not own -- an image, a pod's CPU limits -- and it is deliberately
// no richer than that. The agent knows nothing of workflows, dependencies or
// schedules, exactly as the Executor interface says of every executor.
type StartRequest struct {
	Protocol    int    `json:"protocol"`
	ExecutionID string `json:"execution_id"`
	NodeID      string `json:"node_id"`

	// Identifying, not behavioural: they let an operator on the host see which
	// run a process belongs to, which is the whole reason the pod carries them
	// as labels.
	Workflow string `json:"workflow"`
	RunID    string `json:"run_id"`
	Attempt  int    `json:"attempt"`

	Command string            `json:"command"`
	WorkDir string            `json:"workdir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// Secrets is variable-name -> `secret-name/key`, UNRESOLVED, and it is the
	// point of decision 3.
	//
	// The engine does not hold these values and does not learn them. The agent
	// resolves each coordinate against its own store, under its own allowlist,
	// on the host that administers them. That is what the kubelet does for a
	// pod and what the OS does for a local process; sending resolved values
	// over mTLS instead would protect the wire and do nothing about the
	// dispatcher, the task assembly log, or any TaskExec dump somebody writes
	// later -- which is what `TaskExec.Secrets`' own comment was written about.
	//
	// An agent that cannot resolve one must FAIL the step naming the
	// coordinate. Starting a step with a variable silently unset is how a 401
	// three layers down gets blamed on the wrong service.
	Secrets map[string]string `json:"secrets,omitempty"`

	// TimeoutSeconds is zero for no limit, matching TaskExec.Timeout.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Line kinds. The agent emits one JSON object per line, and the line is the
// framing: the engine's scanner breaks on newlines, the `@brevis:` protocol
// inside a step's output is line-oriented, and SSE's own framing would buy
// nothing on top of that.
const (
	// KindStarted says the process exists on the host. Nothing before it can be
	// attributed to the step.
	KindStarted = "started"

	// KindLog is one line the step wrote. `Stream` keeps stdout apart from
	// stderr, which the pod executor cannot do at all -- Kubernetes merges
	// them -- so this executor is strictly better informed there.
	KindLog = "log"

	// KindAlive is the lease renewal. It carries no information about the step;
	// its arrival IS the information. A step can legitimately print nothing for
	// an hour, so silence cannot mean death and this is what tells the two
	// apart.
	KindAlive = "alive"

	// KindExit ends the stream. Everything after it is ignored.
	KindExit = "exit"

	// KindFailed is the agent reporting it could not run the step at all --
	// an unresolvable secret, a missing working directory, a refused protocol
	// version. Distinct from an exit code because "the step ran and failed" and
	// "the step never ran" are different facts.
	KindFailed = "failed"
)

// Line is one NDJSON record from the agent.
type Line struct {
	// Seq is monotonic from 1, per execution, and never reused. It is what
	// makes a resumed stream safe: see the package comment, decision 1.
	Seq int64 `json:"seq"`

	Kind    string `json:"kind"`
	Stream  string `json:"stream,omitempty"`
	Message string `json:"message,omitempty"`

	// Code is the process's exit status, on a KindExit line.
	Code int `json:"code,omitempty"`
}

// Validate refuses a line that would be worse to act on than to reject.
//
// The agent is a separate program on a separate release cycle, on a host this
// engine does not administer. Trusting its output shape is how a bad deploy over
// there becomes a confusing failure over here.
func (l Line) Validate() error {
	if l.Seq < 1 {
		return fmt.Errorf("line has sequence %d; the agent numbers from 1", l.Seq)
	}
	switch l.Kind {
	case KindStarted, KindLog, KindAlive, KindExit, KindFailed:
		return nil
	}
	return fmt.Errorf("line %d has kind %q, which this engine does not know", l.Seq, l.Kind)
}

// Resume is what the engine sends to pick a stream back up.
type Resume struct {
	Protocol int    `json:"protocol"`
	After    int64  `json:"after"`
	Reason   string `json:"reason,omitempty"`
}

// GapError is the agent saying it can no longer replay from where the engine
// stopped: its ring has moved past that point.
//
// It is a distinct answer rather than a truncated stream because the difference
// matters. A step whose output is missing an unknown number of lines is worse
// than a step that failed -- its counters are wrong by an unknown amount and
// nothing says so.
type GapError struct {
	Wanted    int64 `json:"wanted"`
	Available int64 `json:"available"`
}

func (g GapError) Error() string {
	return fmt.Sprintf("the agent can no longer replay from line %d; its buffer starts at %d. "+
		"The step is failed rather than resumed: an unknown number of missing lines would "+
		"leave its counters wrong by an unknown amount, silently", g.Wanted, g.Available)
}

// DefaultAliveEvery is how often an agent should renew its lease, and
// DefaultLeaseLimit is how long the engine waits before deciding it died.
//
// Three renewals of headroom, which is the ratio the queue's own recovery uses:
// a limit that is a small multiple of the renewal survives one slow moment and
// still catches a dead process quickly. Too short kills a step whose host was
// merely busy; too long is the thing this prevents.
const (
	DefaultAliveEvery = 10 * time.Second
	DefaultLeaseLimit = 30 * time.Second
)

// Decode reads one NDJSON line.
func Decode(raw []byte) (Line, error) {
	var l Line
	if err := json.Unmarshal(raw, &l); err != nil {
		return Line{}, fmt.Errorf("the agent sent a line that is not JSON: %w", err)
	}
	return l, l.Validate()
}
