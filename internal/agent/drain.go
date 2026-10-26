package agent

import "errors"

// ErrDraining is a start refused because this agent is on its way out (#136).
//
// Typed, because it is the one refusal that says nothing about the request:
// the same step would run on another agent, or on this one's replacement. The
// handler answers it 503 rather than 400 for that reason.
var ErrDraining = errors.New("this agent is draining: it finishes the steps it " +
	"has and takes no new one")

// Drain stops this agent taking new steps.
//
// It is the first half of a shutdown and only that: the steps already running
// go on, and so do their resumes and cancels -- an engine that restarted in the
// same rollout comes back for a step it handed over, and a drain that refused
// it would be waiting for a step nobody can read. /health turns 503, so a
// balancer in front of a pool stops sending starts here.
//
// Idempotent: a second signal must not undo or repeat the first.
func (a *Agent) Drain() {
	a.mu.Lock()
	a.draining = true
	a.mu.Unlock()
}

// isDraining reads it back.
func (a *Agent) isDraining() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.draining
}
