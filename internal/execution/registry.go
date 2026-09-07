package execution

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Task is a unit of work written in Go, compiled into the binary.
//
// Section 14 of the plan is categorical: "Do not execute arbitrary code
// received through the API. Local tasks must be compiled and registered in the
// runtime". The registry exists to make that structural — the YAML can only
// name
// something that is already in the binary, never supply the code.
type Task interface {
	Name() string
	Run(ctx context.Context, in Input) error
}

// Input is what the task receives.
type Input struct {
	NodeID string
	With   map[string]any

	// Log emits one line into the event stream. It exists so the task can report
	// progress without knowing about channels or the executor.
	Log func(msg string)
}

// Texto reads a required parameter out of `with`. A convenience with a useful
// error: a task doing the type assertion by hand repeats the same poor
// message.
func (i Input) Text(key string) (string, error) {
	v, ok := i.With[key]
	if !ok {
		return "", fmt.Errorf("parameter %q is missing from `with`", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("parameter %q has to be text, got %T", key, v)
	}
	return s, nil
}

// Registry holds the available tasks. Safe for concurrent use because the
// dispatcher queries it from several goroutines.
type Registry struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

func NewRegistry() *Registry {
	return &Registry{tasks: map[string]Task{}}
}

// Register adds a task.
//
// It refuses a duplicate name rather than overwriting: a silently replaced
// registration is a bug that only shows up in production, when the wrong task
// runs.
func (r *Registry) Register(t Task) error {
	nome := t.Name()
	if nome == "" {
		return fmt.Errorf("task with no name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, existe := r.tasks[nome]; existe {
		return fmt.Errorf("task %q is already registered", nome)
	}
	r.tasks[nome] = t
	return nil
}

// MustRegister registra e entra em panico se falhar.
//
// For use in `init()` or at boot: an invalid registration is a programming
// error, and failing at start beats finding out on the first scheduled run.
func (r *Registry) MustRegister(t Task) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get looks a task up by name.
func (r *Registry) Get(nome string) (Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[nome]
	return t, ok
}

// Nomes lists what is registered, sorted. It serves the unknown-task error:
// saying what does exist saves a trip to the documentation.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.tasks))
	for n := range r.tasks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FuncTask adapts a function to the Task interface, for the cases where a type
// of its own would be ceremony with no gain.
type FuncTask struct {
	TaskName string
	Fn       func(ctx context.Context, in Input) error
}

func (f FuncTask) Name() string { return f.TaskName }
func (f FuncTask) Run(ctx context.Context, in Input) error {
	return f.Fn(ctx, in)
}
