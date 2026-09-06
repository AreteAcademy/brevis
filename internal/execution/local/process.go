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
	rodando map[string]context.CancelFunc
}

// ErrForaDoLocal is returned when the executor is built outside local mode.
// Typed so a test can assert on it.
type ErrForaDoLocal struct{ Env string }

func (e ErrForaDoLocal) Error() string {
	return fmt.Sprintf("ProcessExecutor so opera com BREVIS_ENV=local (recebido %q); "+
		"fora do local, `run:` deve ir para o KubernetesExecutor", e.Env)
}

// New returns the executor, or refuses when the environment is not local.
func New(env string) (*ProcessExecutor, error) {
	if env != "local" {
		return nil, ErrForaDoLocal{Env: env}
	}
	return &ProcessExecutor{shell: "/bin/sh", rodando: map[string]context.CancelFunc{}}, nil
}

// ambienteDaTask assembles the process's env: the literals, plus the secrets
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
func ambienteDaTask(t execution.TaskExec) ([]string, error) {
	env := make(map[string]string, len(t.Env)+len(t.Secrets))
	for k, v := range t.Env {
		env[k] = v
	}

	var faltando []string
	for nome, coord := range t.Secrets {
		v, existe := os.LookupEnv(nome)
		if !existe || v == "" {
			faltando = append(faltando, fmt.Sprintf("%s (secrets: %s)", nome, coord))
			continue
		}
		env[nome] = v
	}
	if len(faltando) > 0 {
		sort.Strings(faltando)
		return nil, fmt.Errorf("task %q: no modo local os segredos vem do ambiente do "+
			"proprio motor, e estes nao estao definidos: %s",
			t.NodeID, strings.Join(faltando, ", "))
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
	p.rodando[t.ExecutionID] = cancel
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
	ambiente, err := ambienteDaTask(t)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Env = ambiente

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

	eventos := make(chan execution.Event, 64)
	go func() {
		defer close(eventos)
		defer func() {
			p.mu.Lock()
			delete(p.rodando, t.ExecutionID)
			p.mu.Unlock()
			cancel()
		}()

		eventos <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); repassar(stdout, "stdout", t.NodeID, eventos) }()
		go func() { defer wg.Done(); repassar(stderr, "stderr", t.NodeID, eventos) }()
		wg.Wait() // drenar ANTES do Wait: fechar os pipes cedo perderia as ultimas linhas

		err := cmd.Wait()
		code := cmd.ProcessState.ExitCode()
		if err != nil {
			eventos <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID,
				ExitCode: code, Err: err,
				Message: fmt.Sprintf("saiu com codigo %d", code),
			}
			return
		}
		eventos <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID, ExitCode: code}
	}()

	return eventos, nil
}

// Cancel interrupts a run in flight.
func (p *ProcessExecutor) Cancel(_ context.Context, execID string) error {
	p.mu.Lock()
	cancel, ok := p.rodando[execID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %q is not running", execID)
	}
	cancel()
	return nil
}

func repassar(r io.Reader, stream, nodeID string, out chan<- execution.Event) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // linhas longas de log nao podem truncar a saida
	for sc.Scan() {
		out <- execution.Event{
			Kind: execution.EventLog, NodeID: nodeID,
			Stream: stream, Message: sc.Text(),
		}
	}
}
