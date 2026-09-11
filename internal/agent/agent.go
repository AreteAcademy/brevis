// Package agent runs steps on behalf of an engine it does not trust.
//
// The other half of internal/execution/remote. The engine hands over a command
// and streams the result back; this is what is on the host.
//
// # It was written second, on purpose
//
// The executor exists and its contract is pinned by thirteen tests against a
// fake. Writing the agent first would have made it the specification by
// accident -- the contract would then be whatever that program happened to do,
// and the engine would have grown to match its quirks. Everything here is
// written to satisfy internal/execution/remote/protocol.go, and the test that
// matters puts the real engine and the real agent on a socket together.
//
// # What it refuses to be
//
// Not an orchestrator. It knows nothing of workflows, dependencies, schedules
// or retries, exactly as the TaskExec comment says of every executor. It takes
// a command, runs it, and reports what happened.
//
// Not a secret store. It RESOLVES secrets from one -- a directory of files,
// the same shape the kubelet mounts and Docker uses -- under an allowlist that
// belongs to this host and not to the engine's cluster. The engine never sends
// a value and never learns one.
package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// Options configure one agent.
type Options struct {
	// Token authenticates the engine. Empty accepts anyone, which is a
	// development convenience and is warned about at startup rather than
	// silently allowed.
	//
	// One token for every engine, and the gap is stated rather than
	// discovered: no revoking one without changing them all, and no per-engine
	// identity in the audit trail. See the package's README for what a second
	// version would need.
	Token string

	// SecretsDir is the root of the secret store: `<dir>/<name>/<key>` holds
	// one value, which is how the kubelet mounts a Secret and how Docker mounts
	// one. Empty means this host serves no secrets at all, and a step that asks
	// for one is refused naming the coordinate.
	SecretsDir string

	// AllowedSecrets limits which secret NAMES a step may ask for.
	//
	// The same division the pod executor has, with the boundary moved: the
	// installation says which secrets exist, the workflow says which step gets
	// which. Empty denies everything -- a host somebody else administers should
	// not hand over its store because an engine asked nicely.
	AllowedSecrets []string

	// WorkDir is where a step runs when it names no directory of its own.
	WorkDir string

	// RingSize is how many lines are kept for a resumed connection.
	//
	// Bounded on purpose, and the consequence is stated rather than implied:
	// this many lines of network outage survive, and beyond it the engine is
	// told the gap is unrecoverable and fails the step. A step missing an
	// unknown number of lines is worse than a step that failed.
	RingSize int

	// AliveEvery is how often a running step emits a heartbeat. Zero takes the
	// protocol's default.
	AliveEvery time.Duration

	// StateDir is where the execID -> pid map is written, so cancel survives
	// this process restarting. Empty keeps it in memory only, and cancel is
	// then best-effort across a restart.
	StateDir string

	// Shell runs the command. Empty is `/bin/sh -c`, matching the local
	// executor: `run:` in the YAML is a shell line, not an argv.
	Shell []string
}

const defaultRingSize = 10000

// Agent holds the running executions.
type Agent struct {
	opt Options

	mu   sync.Mutex
	runs map[string]*execution
}

// New builds an agent.
func New(opt Options) *Agent {
	if opt.RingSize <= 0 {
		opt.RingSize = defaultRingSize
	}
	if opt.AliveEvery <= 0 {
		opt.AliveEvery = remote.DefaultAliveEvery
	}
	if len(opt.Shell) == 0 {
		opt.Shell = []string{"/bin/sh", "-c"}
	}
	return &Agent{opt: opt, runs: map[string]*execution{}}
}

// execution is one step, from spawn to exit.
//
// The lines live here rather than on the connection, which is the whole reason
// a dropped connection can be resumed: the process goes on writing into the
// ring whether anybody is reading it or not.
type execution struct {
	id string

	mu     sync.Mutex
	cond   *sync.Cond
	lines  []remote.Line // the ring, newest last
	first  int64         // the sequence of lines[0]; what a gap is measured against
	next   int64         // the sequence the next line will get
	closed bool

	ring int
	cmd  *exec.Cmd
	stop context.CancelFunc

	// started is the process's start time, kept so a cancel after this agent
	// restarts can refuse a pid the OS has since handed to somebody else.
	// Killing the wrong process on somebody's host is the worst thing this
	// program could do.
	started time.Time
}

// Start runs a step and returns a reader over its stream.
//
// The request is validated BEFORE anything is spawned: a refused step must
// leave no process behind, and an unresolvable secret must be a refusal rather
// than a command running with a variable silently unset.
func (a *Agent) Start(ctx context.Context, req remote.StartRequest) (io.ReadCloser, error) {
	if req.Protocol != remote.Protocol {
		return nil, fmt.Errorf("this agent speaks protocol %d and the engine asked for %d; "+
			"one of the two needs upgrading", remote.Protocol, req.Protocol)
	}
	if req.ExecutionID == "" || req.Command == "" {
		return nil, fmt.Errorf("a step needs an execution id and a command")
	}

	a.mu.Lock()
	if _, running := a.runs[req.ExecutionID]; running {
		a.mu.Unlock()
		return nil, fmt.Errorf("execution %q is already running here; a retry gets a new id",
			req.ExecutionID)
	}
	a.mu.Unlock()

	env, err := a.environment(req)
	if err != nil {
		// Refused before spawning. The engine turns this into a failed step
		// naming the coordinate, which is the point: a command that starts with
		// VENDOR_TOKEN unset produces a 401 three layers down and blames the
		// wrong service.
		return nil, err
	}

	e := &execution{id: req.ExecutionID, ring: a.opt.RingSize, first: 1, next: 1}
	e.cond = sync.NewCond(&e.mu)

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if req.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	e.stop = cancel

	dir := req.WorkDir
	if dir == "" {
		dir = a.opt.WorkDir
	}
	cmd := exec.CommandContext(runCtx, a.opt.Shell[0], append(a.opt.Shell[1:], req.Command)...)
	cmd.Dir = dir
	cmd.Env = env
	// Its own process group, so cancelling kills the shell AND whatever it
	// spawned. Killing only the shell leaves a python that goes on writing to a
	// pipe nobody reads.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		return nil, fmt.Errorf("starting the step on this host: %w", err)
	}
	e.cmd = cmd
	e.started = time.Now()

	a.mu.Lock()
	a.runs[req.ExecutionID] = e
	a.mu.Unlock()
	a.remember(req.ExecutionID, cmd.Process.Pid, e.started)

	e.emit(remote.Line{Kind: remote.KindStarted})

	var streams sync.WaitGroup
	streams.Add(2)
	go func() { defer streams.Done(); e.copy(stdout, "stdout") }()
	go func() { defer streams.Done(); e.copy(stderr, "stderr") }()

	heartbeat := make(chan struct{})
	go e.beat(a.opt.AliveEvery, heartbeat)

	go func() {
		streams.Wait()
		err := cmd.Wait()
		close(heartbeat)
		cancel()

		a.mu.Lock()
		delete(a.runs, req.ExecutionID)
		a.mu.Unlock()
		a.forget(req.ExecutionID)

		e.emit(remote.Line{Kind: remote.KindExit, Code: exitCode(err)})
		e.close()
	}()

	return e.reader(0), nil
}

// Resume serves the same execution from `after`.
//
// A gap is answered rather than papered over: when the ring no longer reaches
// back that far the engine is told what it wanted and what is available, and it
// fails the step. Serving from the oldest line instead would hand back a stream
// with a hole in the middle and no way to know how big.
func (a *Agent) Resume(execID string, after int64) (io.ReadCloser, error) {
	a.mu.Lock()
	e := a.runs[execID]
	a.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("execution %q is not running here; it finished, or this "+
			"agent restarted and lost it", execID)
	}

	e.mu.Lock()
	first := e.first
	e.mu.Unlock()
	if after+1 < first {
		return nil, remote.GapError{Wanted: after + 1, Available: first}
	}
	return e.reader(after), nil
}

// Cancel stops a step.
//
// The process GROUP is signalled, not the process: the shell spawned whatever
// the command is, and killing only the shell leaves it running.
func (a *Agent) Cancel(execID string) error {
	a.mu.Lock()
	e := a.runs[execID]
	a.mu.Unlock()

	if e == nil {
		// Not in memory. Either it finished -- in which case cancelling is a
		// no-op and saying so is honest -- or this agent restarted, and the
		// file is what is left of it.
		return a.cancelFromState(execID)
	}
	e.stop()
	if e.cmd != nil && e.cmd.Process != nil {
		_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGTERM)
	}
	return nil
}

// environment resolves the step's variables, secrets included.
//
// The ordering matters and matches the local executor's: the plain environment
// first, then the secrets, so a `secrets:` entry can deliberately override an
// ordinary variable of the same name. That is what "this one, on purpose"
// means.
func (a *Agent) environment(req remote.StartRequest) ([]string, error) {
	env := map[string]string{}
	for k, v := range req.Env {
		env[k] = v
	}

	var missing []string
	for name, coord := range req.Secrets {
		value, err := a.secret(coord)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s): %v", name, coord, err))
			continue
		}
		env[name] = value
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("this host could not resolve %d secret(s) and the step was "+
			"NOT started: %s", len(missing), strings.Join(missing, "; "))
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	// Sorted, so two identical steps get the same environment and a diff
	// between two runs is not map-ordering noise. The local executor does the
	// same.
	sort.Strings(out)
	return out, nil
}

// secret reads one `name/key` out of the store.
func (a *Agent) secret(coord string) (string, error) {
	name, key, ok := strings.Cut(coord, "/")
	if !ok || name == "" || key == "" {
		return "", fmt.Errorf("%q is not `secret-name/key`", coord)
	}
	if !a.allowed(name) {
		if len(a.opt.AllowedSecrets) == 0 {
			return "", fmt.Errorf("no secret is allowed on this host, and neither is this "+
				"one: the host decides, in --allow-secrets (asked for %q)", name)
		}
		return "", fmt.Errorf("%q is not in this host's --allow-secrets (allowed: %s)",
			name, strings.Join(a.opt.AllowedSecrets, ", "))
	}
	if a.opt.SecretsDir == "" {
		return "", fmt.Errorf("this host serves no secrets: --secrets-dir is unset")
	}
	// TWO guards, and they are redundant ON PURPOSE.
	//
	// The name arrives from a workflow file somebody else wrote, and this is
	// the one place a string from that file becomes a path on a host this
	// program does not own. `../private` as a secret name must not read outside
	// the store.
	//
	// `filepath.Clean("/"+name)` turns `..` into `/`, so the Join stays inside.
	// The HasPrefix then checks the result anyway. Either one alone stops the
	// traversal -- proved by removing each and watching the test stay green --
	// and removing BOTH leaks the file, which is what makes this defence in
	// depth rather than a dead branch. Do not "simplify" it to one.
	path := filepath.Join(a.opt.SecretsDir, filepath.Clean("/"+name), filepath.Clean("/"+key))
	if !strings.HasPrefix(path, filepath.Clean(a.opt.SecretsDir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("%q resolves outside the secret store", coord)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("not in this host's store")
	}
	// Trailing newline trimmed: a value written with `echo` has one, and a
	// token with a newline on the end is a 401 nobody can see.
	return strings.TrimRight(string(raw), "\r\n"), nil
}

func (a *Agent) allowed(name string) bool {
	for _, p := range a.opt.AllowedSecrets {
		if p == name {
			return true
		}
	}
	return false
}

// exitCode reads the status out of what Wait returned.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if ok := asExit(err, &ee); ok {
		return ee.ExitCode()
	}
	// Something other than the process failing: a missing shell, a working
	// directory that is not there. Non-zero, and the engine reports it as a
	// failed step rather than inventing a code.
	return -1
}
