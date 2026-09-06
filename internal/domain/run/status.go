// Package run is the domain model of a run and its steps.
//
// Section 7 of the plan is explicit: "do not use plain booleans such as
// running = true". States are a type, and transitions are validated — a `Run`
// cannot go from SUCCESS to RUNNING through carelessness or through a race.
package run

import "fmt"

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
)

// transicoes declares section 7's graph. Keeping it as data, and not as a chain
// of ifs, makes the machine inspectable and the exhaustive test trivial.
var transicoes = map[Status][]Status{
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

// Terminal says whether the state ends the run's life.
//
// FAILED is not terminal: it can go to RETRYING. What decides whether there is
// an attempt left is the retry policy, not the state machine.
func (s Status) Terminal() bool {
	return s == StatusSuccess || s == StatusCanceled
}

// CanGo says whether the transition is allowed.
func (s Status) CanGo(destino Status) bool {
	for _, d := range transicoes[s] {
		if d == destino {
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

// Validate returns an error when the transition does not exist in the graph.
func Validate(de, para Status) error {
	if _, conhecido := transicoes[de]; !conhecido {
		return fmt.Errorf("unknown state: %q", de)
	}
	if !de.CanGo(para) {
		return ErrInvalidTransition{De: de, Para: para}
	}
	return nil
}
