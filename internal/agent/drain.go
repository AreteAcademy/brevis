package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

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
// What it will wait for is fixed HERE, under the lock the reservation takes:
// every execution in `runs` at this instant -- one still being set up
// included -- and nothing after, because nothing after is let in.
//
// Idempotent: a second signal must not undo or repeat the first.
func (a *Agent) Drain() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.draining {
		return
	}
	a.draining = true
	a.awaited = make(map[string]*execution, len(a.runs))
	for id, e := range a.runs {
		a.awaited[id] = e
	}
}

// isDraining reads it back.
func (a *Agent) isDraining() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.draining
}

// Wait returns when every step running at Drain has ended AND been read to its
// exit, or when ctx ends -- naming, then, what it was still waiting for.
//
// Ended is not enough. A step that ends while the engine is away keeps its exit
// only in this process's memory; leaving then loses it, and the engine coming
// back is answered "unknown execution" and starts the step AGAIN under the
// same id (#125). So the drain waits for somebody to read it: the engine's
// resume, which is what the rollout that restarts it is about to send.
//
// A step that was cancelled, or abandoned before it spawned, is not waited
// for: whoever cancelled already knows how it ends, and an abandoned start was
// answered with its refusal.
func (a *Agent) Wait(ctx context.Context) error {
	for {
		a.mu.Lock()
		changed := a.changed
		ids := a.pendingLocked()
		a.mu.Unlock()
		if len(ids) == 0 {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("still waiting for %d step(s) to end and be read (%s): %w",
				len(ids), strings.Join(ids, ", "), ctx.Err())
		}
	}
}

// pending lists what the drain is still waiting for.
func (a *Agent) pending() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pendingLocked()
}

// pendingLocked is pending with a.mu held. It takes each execution's lock
// inside the agent's, the one order settle never inverts.
func (a *Agent) pendingLocked() []string {
	var ids []string
	for id, e := range a.awaited {
		e.mu.Lock()
		settled := e.settled
		e.mu.Unlock()
		if !settled {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// settle marks an execution as no longer owed to anybody -- its exit read,
// or it was cancelled or abandoned -- and wakes the drain.
//
// The execution's lock is let go BEFORE the agent's is taken: pendingLocked
// holds the agent's and then takes the execution's, and taking them here in
// the other order is a deadlock waiting for its moment.
func (a *Agent) settle(e *execution) {
	e.mu.Lock()
	already := e.settled
	e.settled = true
	e.mu.Unlock()
	if already {
		return
	}
	a.mu.Lock()
	close(a.changed)
	a.changed = make(chan struct{})
	a.mu.Unlock()
}

// reading hands out a reader over e that settles it when it is read to the
// end -- through the exit line -- on a connection that took every byte.
func (a *Agent) reading(e *execution, after int64) io.ReadCloser {
	r := e.reader(after)
	r.end = func() { a.settle(e) }
	return r
}
