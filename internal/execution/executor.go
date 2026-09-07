// Package execution defines the contract for executing tasks.
//
// The interface is section 13's, with one difference: `Execute` returns a
// channel of events instead of blocking. A pod that runs for twenty minutes has
// to report progress before it ends, and the same holds for a local process
// writing to stdout.
package execution

import (
	"context"
	"time"
)

// Executor runs a task and reports what happens.
type Executor interface {
	Name() string
	Execute(ctx context.Context, t TaskExec) (<-chan Event, error)
	Cancel(ctx context.Context, execID string) error
}

// TaskExec is what gets asked to run. Deliberately poor: the executor knows
// nothing of workflows, dependencies or schedules.
//
// `Command` and `Action` are exclusive: the first goes to the ProcessExecutor,
// the second resolves in the Go task registry. The runner is what chooses the
// executor, not the executor itself.
type TaskExec struct {
	ExecutionID string
	NodeID      string

	// Workflow, RunID and Attempt do not change the run — they identify it.
	// In Kubernetes they become the pod's labels, and they are what makes it
	// possible to find "that run's pods" without searching by name.
	Workflow string
	RunID    string

	// The STEP's attempt, within one execution of the run.
	Attempt int

	// TentativaDoRun is the RUN's, counted by the dispatcher. Both go into the
	// pod's name: without the second, a dispatcher retry recreates the run from
	// scratch (the step at attempt 0 again) and runs into the previous pod.
	RunAttempt int

	Command string // a shell line, for the ProcessExecutor
	Action  string // the name in the registry, for the GoExecutor
	With    map[string]any

	// Image is this step's runtime. Empty in local mode (the command runs on the
	// instance itself); required in Kubernetes, where it IS the pod.
	Image string

	// Shell chooses between `sh -c "line"` and plain argv. It matters for a
	// distroless image, which has no shell at all.
	Shell bool

	// The pod's resources, in Kubernetes's format. Ignored in local mode, where
	// a process's limit is the machine's.
	CPU, Memoria       string
	CPUMax, MemoriaMax string

	WorkDir string
	Env     map[string]string

	// Secrets are variables whose value the engine does NOT carry: the map is
	// variable-name -> `secret-name/key`, and the executor is what resolves it.
	//
	// They are kept apart from Env for exactly that reason. If the value arrived
	// resolved here, it would pass through the dispatcher, through the task
	// assembly log and through any TaskExec dump somebody writes later.
	Secrets map[string]string

	// OutputPath is where the step writes what it publishes for the steps that
	// depend on it. It reaches the step as BREVIS_OUTPUT.
	//
	// The RUNNER picks it and the EXECUTOR may override it, because only the
	// executor knows what a path means in its world: a temporary file in the
	// engine's filesystem is meaningless inside a pod, where the answer is
	// /dev/termination-log -- which the engine already reads to get the exit
	// code.
	OutputPath string

	// A zero Timeout means no limit. Section 37 asks for a timeout in PHASE 3;
	// leaving the default open is deliberate — imposing an arbitrary limit would
	// kill legitimately long tasks.
	Timeout time.Duration
}

// EventKind classifies what the executor reports.
type EventKind string

const (
	EventStarted   EventKind = "started"
	EventLog       EventKind = "log"
	EventSucceeded EventKind = "succeeded"

	// EventContext carries what the step published, in Message.
	//
	// It travels as an event for the same reason the phases do: the runner
	// collects it without knowing whether it came from a file on this disk or
	// from a pod's termination message, so a third executor costs the runner
	// nothing.
	EventContext EventKind = "context"
	EventFailed  EventKind = "failed"
)

// Event is one occurrence during the run. `Stream` tells stdout from stderr:
// merging the two loses the information about where the message came from, and
// that is exactly what made dbt's final summary show up as an error in
// Leoflow.
type Event struct {
	Kind     EventKind
	NodeID   string
	Message  string
	Stream   string // "stdout" | "stderr"
	ExitCode int
	Err      error
}
