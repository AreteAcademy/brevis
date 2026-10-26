package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
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

// How long the end of a shutdown is given: the cancelled steps to write their
// exits, then the server to close what is still open. Short, because by then
// the drain is over and whatever bounds this process -- the orchestrator's
// grace period -- is already running out.
const (
	cancelGrace   = 10 * time.Second
	shutdownGrace = 10 * time.Second
)

// drainReportEvery is how often a drain says what it is still waiting for, so
// one that lasts an hour does not look like a process that hung.
const drainReportEvery = time.Minute

// Serve runs srv on ln until stop closes, then drains, and returns when the
// listener is closed -- with how many steps it gave up on: cancelled while
// running, or ended with an exit nobody read. main exits non-zero on any.
//
// HERE and not in main, for the reason Options.Check is: a shutdown that only
// exists inside `func main` is a shutdown nothing tests, and the one main had
// logged "running steps keep their streams" and then exited with them (#136).
//
// The server stays up through the drain. srv.Shutdown first would close the
// listener, and the resume the drain is waiting for -- from an engine that
// restarted in the same rollout -- would find nothing to connect to. It would
// also wait on the wrong thing: connections, while a step does not live on
// one.
//
// The drain ends when every step running at stop has ended and been read
// (Wait). A drainTimeout of zero is no limit, the orchestrator's grace period
// being the real one; force closing -- a second signal -- ends it as the
// timeout would. Either way what is left is cancelled, the whole process
// group as /cancel does: a step left behind by an agent that exited is out of
// anybody's reach.
func (a *Agent) Serve(srv *http.Server, ln net.Listener, stop, force <-chan struct{},
	drainTimeout time.Duration, log *slog.Logger) (int, error) {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		// The listener failed on its own; there is nothing to drain into.
		return 0, err
	case <-stop:
	}

	a.Drain()
	log.Info("draining: no new steps; waiting for the running ones to end and be read",
		"running", a.pending(), "timeout", drainTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	if drainTimeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), drainTimeout)
	}
	defer cancel()
	go func() {
		report := time.NewTicker(drainReportEvery)
		defer report.Stop()
		for {
			select {
			case <-force:
				log.Warn("a second signal: ending the drain now")
				cancel()
				return
			case <-report.C:
				log.Info("still draining", "waiting for", a.pending())
			case <-ctx.Done():
				return
			}
		}
	}()

	cancelled := 0
	if err := a.Wait(ctx); err != nil {
		left := a.pending()
		log.Warn("the drain did not finish; cancelling what is left", "steps", left, "error", err)
		for _, id := range left {
			a.mu.Lock()
			_, running := a.runs[id]
			a.mu.Unlock()
			if err := a.Cancel(id); err != nil {
				log.Error("a step could not be cancelled", "execution", id, "error", err)
				continue
			}
			if running {
				log.Warn("step cancelled by the shutdown", "execution", id)
			} else {
				// Ended, and nobody came for it: its exit leaves with this
				// process, and the engine will run it again.
				log.Warn("step ended but its exit was never read; it is lost", "execution", id)
			}
		}
		cancelled = len(left)
		a.idle(cancelGrace)
	}

	shutdown, stopShutdown := context.WithTimeout(context.Background(), shutdownGrace)
	defer stopShutdown()
	if err := srv.Shutdown(shutdown); err != nil {
		// A stream still open past the grace: nothing it carries is owed to
		// anybody any more, so it is cut.
		_ = srv.Close()
	}
	log.Info("drained", "cancelled", cancelled)
	return cancelled, nil
}

// idle waits, up to d, for no step to be running: the cancelled ones to have
// been waited for and their exits written, so the process does not leave
// with a signal still in flight.
func (a *Agent) idle(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(a.runs)
		a.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
