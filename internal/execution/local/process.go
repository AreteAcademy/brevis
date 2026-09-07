// Package local implements running processes on the host.
//
// It exists because of the 2026-08-31 amendment to section 3 of the plan: the
// original text required Kubernetes for any language that was not Go, which made
// developing on the instance itself impossible.
//
// The boundary is code, not convention: New refuses to build the executor
// outside local mode. A `run:` with no declared limit is the real risk — not the
// command.
package local

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// ProcessExecutor runs arbitrary commands as host processes.
type ProcessExecutor struct {
	shell string

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// ErrOutsideLocal is returned when the executor is built outside local mode.
// Typed so a test can assert on it.
type ErrOutsideLocal struct{ Env string }

func (e ErrOutsideLocal) Error() string {
	return fmt.Sprintf("ProcessExecutor only operates with BREVIS_ENV=local (got %q); "+
		"outside local, `run:` has to go to the KubernetesExecutor", e.Env)
}

// New returns the executor, or refuses when the environment is not local.
func New(env string) (*ProcessExecutor, error) {
	if env != "local" {
		return nil, ErrOutsideLocal{Env: env}
	}
	return &ProcessExecutor{shell: "/bin/sh", running: map[string]context.CancelFunc{}}, nil
}

// taskEnvironment assembles the process's env: the literals, plus the secrets
// read out of the engine's own environment.
//
// In Kubernetes the coordinate `gabriel-session/cookie` points at a Secret. Here
// there is no Secret at all, so the engine reads the SAME-NAMED variable out of
// its own environment. The asymmetry is deliberate and is documented in the
// domain; what it must not do is fail in silence.
//
// Absent is an ERROR, and not an empty string. An empty GABRIEL_SESSION_COOKIE
// becomes an empty cookie header and a 401 further down, blaming the API for a
// variable nobody exported.
func taskEnvironment(t execution.TaskExec) ([]string, error) {
	env := make(map[string]string, len(t.Env)+len(t.Secrets))
	for k, v := range t.Env {
		env[k] = v
	}

	var missing []string
	for name, coord := range t.Secrets {
		v, existe := os.LookupEnv(name)
		if !existe || v == "" {
			missing = append(missing, fmt.Sprintf("%s (secrets: %s)", name, coord))
			continue
		}
		env[name] = v
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("task %q: no modo local os segredos vem do ambiente do "+
			"engine itself, and these are not set: %s",
			t.NodeID, strings.Join(missing, ", "))
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	// Sorted so two identical processes get the same env: without it, a diff
	// between two runs becomes map-ordering noise.
	sort.Strings(out)
	return out, nil
}

func (p *ProcessExecutor) Name() string { return "process" }

// Execute fires the command and returns the event channel. The channel closes
// when the process ends — the consumer can `range` over it with no extra
// coordination.
func (p *ProcessExecutor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	if t.Command == "" {
		return nil, fmt.Errorf("task %q has no command", t.NodeID)
	}

	ctx, cancel := context.WithCancel(ctx)
	if t.Timeout > 0 {
		// CommandContext kills the process when the context expires, so the
		// timeout covers the whole command and not just the wait.
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
	}

	p.mu.Lock()
	p.running[t.ExecutionID] = cancel
	p.mu.Unlock()

	// `sh -c` because the YAML declares a shell line ("python fetch.py"), not an
	// argv. Accepting the line is the point of `run:`.
	cmd := exec.CommandContext(ctx, p.shell, "-c", t.Command)
	cmd.Dir = t.WorkDir

	// An explicit environment, without inheriting the parent process's: the
	// orchestrator carries credentials a task must not see by accident.
	//
	// `secrets:` is precisely the named opt-in against that rule -- "this one,
	// on purpose" -- which is why it resolves AFTERWARDS, and can override.
	environment, err := taskEnvironment(t)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Env = environment

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("iniciando %q: %w", t.NodeID, err)
	}

	events := make(chan execution.Event, 64)
	go func() {
		defer close(events)
		defer func() {
			p.mu.Lock()
			delete(p.running, t.ExecutionID)
			p.mu.Unlock()
			cancel()
		}()

		events <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); forward(stdout, "stdout", t.NodeID, events) }()
		go func() { defer wg.Done(); forward(stderr, "stderr", t.NodeID, events) }()
		wg.Wait() // drain BEFORE Wait: closing the pipes early would lose the last lines

		err := cmd.Wait()
		code := cmd.ProcessState.ExitCode()

		// What the step published, read BEFORE the outcome is reported.
		//
		// It is read on failure too, on purpose: a step that publishes and then
		// fails has said something true up to that point, and the engine
		// decides what to keep with it. Reading only on success would throw
		// away the one clue a failing step left behind.
		if out := readPublished(t.OutputPath); out != "" {
			events <- execution.Event{
				Kind: execution.EventContext, NodeID: t.NodeID, Message: out,
			}
		}

		if err != nil {
			events <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID,
				ExitCode: code, Err: err,
				Message: fmt.Sprintf("exited with code %d", code),
			}
			return
		}
		events <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID, ExitCode: code}
	}()

	return events, nil
}

// readPublished reads what the step wrote to BREVIS_OUTPUT.
//
// An absent file is the NORMAL case -- most steps publish nothing -- so it is
// silent. An unreadable one is silent too, and that is deliberate: the runner
// is what turns a bad payload into a message, because it is the side that knows
// whether anything downstream was going to read it.
func readPublished(path string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Cancel interrupts a run in flight.
func (p *ProcessExecutor) Cancel(_ context.Context, execID string) error {
	p.mu.Lock()
	cancel, ok := p.running[execID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %q is not running", execID)
	}
	cancel()
	return nil
}

func forward(r io.Reader, stream, nodeID string, out chan<- execution.Event) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // long log lines must not truncate the output
	for sc.Scan() {
		out <- execution.Event{
			Kind: execution.EventLog, NodeID: nodeID,
			Stream: stream, Message: sc.Text(),
		}
	}
}
