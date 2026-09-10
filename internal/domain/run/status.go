// Package run is the domain model of a run and its steps.
//
// Section 7 of the plan is explicit: "do not use plain booleans such as
// running = true". States are a type, and transitions are validated — a `Run`
// cannot go from SUCCESS to RUNNING through carelessness or through a race.
package run

import (
	"fmt"
	"sort"
)

// Status is a run's state.
type Status string

const (
	StatusCreated  Status = "created"
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusRetrying Status = "retrying"
	StatusCanceled Status = "canceled"

	// StatusPending and StatusSkipped are STEP states. A run is never either
	// one, and the two maps below are what enforce that.
	//
	// StatusPending is a step that has not started. It has always existed as
	// the string "pending" in the graph payload and in the screen's palette,
	// with no constant behind it -- which is how the UI ended up owning a state
	// the domain did not know about.
	StatusPending Status = "pending"

	// StatusSkipped is a step that did not run because its trigger rule was not
	// satisfied.
	//
	// It is a state of its own and not a flavour of success or failure, and
	// that distinction is the reason this state exists at all. A step whose
	// upstream failed did not fail -- it was never given the chance -- and
	// calling it success is a lie that reaches the run's own status and the
	// screen. It is not `canceled` either: nobody stopped it. And it is not
	// `pending`, which means "has not run YET" and is the state it would
	// otherwise be left in forever.
	StatusSkipped Status = "skipped"
)

// transitions declares section 7's graph, for a RUN. Keeping it as data, and
// not as a chain of ifs, makes the machine inspectable and the exhaustive test
// trivial.
//
// It does NOT describe a step. The two coincided until `skipped` arrived, which
// is a state a step has and a run does not -- so a single map could no longer
// say the truth about both. See stepTransitions.
var transitions = map[Status][]Status{
	StatusCreated:  {StatusQueued, StatusCanceled},
	StatusQueued:   {StatusRunning, StatusCanceled},
	StatusRunning:  {StatusSuccess, StatusFailed, StatusCanceled},
	StatusFailed:   {StatusRetrying},
	StatusRetrying: {StatusQueued, StatusCanceled},

	// terminal: no way out. SUCCESS does not come back, and neither does
	// CANCELED — re-running creates a new Run, preserving the previous one's
	// history.
	StatusSuccess:  {},
	StatusCanceled: {},
}

// stepTransitions is a STEP's life, and the differences from a run's are the
// point of it existing:
//
//   - PENDING is where a step starts, before any row exists for it.
//   - SKIPPED is reachable from pending and from nowhere else: a step whose
//     rule was not satisfied never started, so there is nothing to interrupt.
//   - RETRYING is absent. A step's retries are a loop inside the runner and
//     each attempt is a row of its own, so the step never occupies a state
//     meaning "about to try again".
//
// A run whose every step was skipped is a SUCCESS. Nothing failed.
var stepTransitions = map[Status][]Status{
	StatusPending: {StatusRunning, StatusSkipped, StatusCanceled},
	StatusRunning: {StatusSuccess, StatusFailed, StatusCanceled},

	StatusSuccess:  {},
	StatusFailed:   {},
	StatusCanceled: {},
	StatusSkipped:  {},
}

// Terminal says whether the state ends a RUN's life.
//
// FAILED is not terminal for a run: it can go to RETRYING. What decides whether
// there is an attempt left is the retry policy, not the state machine.
//
// Derived from the map rather than listed again, so the two cannot disagree.
func (s Status) Terminal() bool { return noWayOut(transitions, s) }

// TerminalStep says whether a STEP is finished, and it differs from Terminal in
// exactly one place: a step's FAILED is final.
//
// A step's retries are a loop inside the runner with a row per attempt, so the
// row itself never goes anywhere. A run's failure can become a retry, and that
// is why the two questions have different answers -- which was found by a
// trigger rule reading `all_done` and deciding a failed step had not finished.
func (s Status) TerminalStep() bool { return noWayOut(stepTransitions, s) }

// InFlight says the system may still act on this run: a dispatcher may claim
// it, a runner may be inside it, a retry may pick it up.
//
// It is NOT the complement of Terminal, and the gap between them is the reason
// this exists. A run that FAILED is not terminal -- the state machine lets it
// become `retrying`, which is what makes a retry possible at all -- but nothing
// is going to retry a run from last March on its own. Retention needs "is
// anybody still holding this", and asking Terminal would keep every failure
// ever recorded, forever, which is the opposite of what a failure deserves.
//
// Enumerated rather than derived, because the question is about OWNERSHIP and
// not about the shape of the graph. TestEveryRunStatusIsClassified is what
// keeps that honest: a new status has to be put on one side or the other before
// it compiles green.
func (s Status) InFlight() bool {
	switch s {
	case StatusCreated, StatusQueued, StatusRunning, StatusRetrying:
		return true
	}
	return false
}

// RunStatuses is every state a RUN can be in.
//
// It exists so code outside this package can enumerate them -- the retention
// query builds its status list from here rather than typing three strings into
// SQL, which is the shape that drifts the day a fourth one arrives.
func RunStatuses() []Status {
	out := make([]Status, 0, len(transitions))
	for s := range transitions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Settled is every run state nothing will touch again: the complement of
// InFlight, and what retention is allowed to delete.
func Settled() []Status {
	var out []Status
	for _, s := range RunStatuses() {
		if !s.InFlight() {
			out = append(out, s)
		}
	}
	return out
}

func noWayOut(graph map[Status][]Status, s Status) bool {
	edges, known := graph[s]
	return known && len(edges) == 0
}

// CanGo says whether the transition is allowed for a RUN.
func (s Status) CanGo(to Status) bool { return allowed(transitions, s, to) }

// CanStepGo says whether the transition is allowed for a STEP.
func (s Status) CanStepGo(to Status) bool { return allowed(stepTransitions, s, to) }

func allowed(graph map[Status][]Status, from, to Status) bool {
	for _, d := range graph[from] {
		if d == to {
			return true
		}
	}
	return false
}

// ErrInvalidTransition carries both states so the error says what happened, and
// not merely that something was refused.
type ErrInvalidTransition struct {
	De, Para Status
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("transicao invalida: %s -> %s", e.De, e.Para)
}

// Validate returns an error when a RUN's transition does not exist in the
// graph.
func Validate(de, para Status) error { return check(transitions, de, para) }

// ValidateStep is the same for a STEP. A step that has not started is
// StatusPending, which is where a skipped one comes from.
func ValidateStep(de, para Status) error { return check(stepTransitions, de, para) }

func check(graph map[Status][]Status, de, para Status) error {
	if _, known := graph[de]; !known {
		return fmt.Errorf("unknown state: %q", de)
	}
	if !allowed(graph, de, para) {
		return ErrInvalidTransition{De: de, Para: para}
	}
	return nil
}
