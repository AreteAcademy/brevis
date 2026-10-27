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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// Options configure one agent.
type Options struct {
	// Token authenticates the engine. Empty is REFUSED unless InsecureNoToken
	// says so, because an agent with no token runs any command for any caller.
	//
	// One token for every engine, and the gap is stated rather than
	// discovered: no revoking one without changing them all, and no per-engine
	// identity in the audit trail. See the package's README for what a second
	// version would need.
	Token string

	// InsecureNoToken allows an agent with no token at all.
	//
	// It exists so the dangerous choice is a DECLARATION and not an omission.
	// Without a token this process runs any command anybody who reaches the
	// port sends it, as whatever user it is -- not a weak password, no
	// password. That used to be allowed with a warning at startup, which is
	// the shape of a hole nobody sees: the warning scrolls past and the
	// manifest that caused it says nothing at all.
	//
	// Now it has to be written down. It shows up in `ps`, in the manifest, and
	// in a review, which is the whole of what this field buys.
	InsecureNoToken bool

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

	// Advertise is the address the engine should come back to for this
	// agent's own executions: `https://dbt-runner-2.dbt.svc:9443`.
	//
	// It matters only behind a SHARED address. An agent is stateful per
	// execution -- a ring and a process handle in this process's memory -- so
	// a resume or a cancel that reaches a different replica is answered with
	// "not running here", and the engine fails a step that is still running
	// over here. See remote.HeaderInstance.
	//
	// TOLD, not discovered. This process can read its hostname; it cannot know
	// which of its names the engine can route to, and a guess that resolves
	// nowhere is worse than no answer -- the engine would address an instance
	// that does not exist instead of falling back to the address it has.
	//
	// Empty is the ordinary case and changes nothing: one agent at one address.
	Advertise string

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

	// StateDir is what survives this process restarting: the execID -> pid
	// map, so cancel still reaches a running step, and the finished
	// executions, so a resume is still answered (#137). Empty keeps both in
	// memory only: cancel is then best-effort across a restart, and a
	// finished execution is forgotten by one.
	StateDir string

	// Shell runs the command. Empty is `/bin/sh -c`, matching the local
	// executor: `run:` in the YAML is a shell line, not an argv.
	Shell []string

	// KeepFinished is how long an execution stays resumable after its process
	// exits, so a scheduler that was down while a step ended can still read
	// how it ended (#125). Zero takes defaultKeepFinished.
	KeepFinished time.Duration

	// KeepFinishedMax bounds how many finished executions are kept, each with
	// its ring; past it the oldest goes first. Zero takes
	// defaultKeepFinishedMax.
	KeepFinishedMax int

	// Now is the clock retention is measured on. Nil is time.Now; a test sets
	// it so an hour of retention takes no hour.
	Now func() time.Time
}

const defaultRingSize = 10000

// The retention of finished executions (#125). An hour covers a scheduler
// restart and a deploy with margin; two hundred bounds the memory, a ring per
// entry, on a host that runs many short steps.
const (
	defaultKeepFinished    = time.Hour
	defaultKeepFinishedMax = 200
)

// Agent holds the running executions.
type Agent struct {
	opt Options

	mu   sync.Mutex
	runs map[string]*execution

	// finished holds executions whose process has exited, so a resume that
	// arrives after the end still gets it (#125): a scheduler that was down
	// while a long step ended learns its exit code, its published context and
	// the end of its log here, or nowhere. An id is in `runs` or in `finished`
	// at every instant from Start on -- never in neither.
	finished map[string]*execution

	// draining refuses new starts (#136). Under `mu`, the lock the reservation
	// takes, so no start slips in between the check and the flip.
	draining bool

	// awaited is what the drain waits for: `runs` as it was at Drain. changed
	// is closed and replaced every time one of them settles, so Wait sleeps
	// rather than polls.
	awaited map[string]*execution
	changed chan struct{}

	// log is for what the agent says outside a request: a finished step it
	// could not write to the state directory. Restore sets it.
	log *slog.Logger
}

// New builds an agent.
// Check refuses a configuration that should not start.
//
// Here rather than in main so a test can reach it: a rule that only exists
// inside a `func main` is a rule nothing exercises.
func (o Options) Check() error {
	if o.Token != "" && o.InsecureNoToken {
		return fmt.Errorf("this agent was given both a token and " +
			"--insecure-no-token, and they are opposites. One of the two is a " +
			"lie and nothing here can say which: drop the flag to use the " +
			"token, or drop the token to mean it")
	}
	if o.Token == "" && !o.InsecureNoToken {
		return fmt.Errorf("this agent has no token, so anything that can reach " +
			"its port would run any command it sends -- as this user, on this " +
			"host. Point --token-file at a file holding one, shared with the " +
			"engine's BREVIS_HOST_TOKEN.\n\nIf an open agent is what you mean " +
			"-- a laptop, a throwaway cluster -- say so with " +
			"--insecure-no-token, so the choice is in the command line and in " +
			"the manifest rather than in the absence of a flag")
	}
	return nil
}

// Advertise sets where the engine should come back to for this agent's
// executions, after construction.
//
// After, because an address is not always known at construction: a test starts
// the server to learn its address, and a pod may be told by an init step. It
// is a plain setter under the same lock the runs map uses, so a handler
// serving a request while this is called reads one value or the other and
// never a torn one.
func (a *Agent) Advertise(addr string) {
	a.mu.Lock()
	a.opt.Advertise = addr
	a.mu.Unlock()
}

// advertising reads it back.
func (a *Agent) advertising() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opt.Advertise
}

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
	if opt.KeepFinished <= 0 {
		opt.KeepFinished = defaultKeepFinished
	}
	if opt.KeepFinishedMax <= 0 {
		opt.KeepFinishedMax = defaultKeepFinishedMax
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Agent{opt: opt, runs: map[string]*execution{}, finished: map[string]*execution{},
		changed: make(chan struct{}), log: discard}
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

	// exited is set, under mu, the moment the process has been waited for.
	// The execution stays in `runs` a little longer -- until its exit line is
	// written -- and a cancel in that stretch must not signal the group: its
	// pid is free for the OS to hand to somebody else.
	exited bool

	// finishedAt is when it moved to the finished set, set under the agent's
	// lock; retention is measured from it.
	finishedAt time.Time

	// settled is set, under mu, once nobody is owed this execution any more:
	// its exit was read to the end, or it was cancelled or abandoned. A drain
	// waits for it (#136).
	settled bool
}

// prune drops the finished executions past their retention, and then the
// oldest past the maximum. The caller holds a.mu.
//
// Run where the set is touched -- an execution finishing, one being looked up
// -- rather than on a ticker: no goroutine to stop on shutdown, and a set
// nobody touches costs nothing but the memory it already had.
func (a *Agent) prune() {
	cutoff := a.opt.Now().Add(-a.opt.KeepFinished)
	for id, e := range a.finished {
		if e.finishedAt.Before(cutoff) {
			a.drop(id)
		}
	}
	for len(a.finished) > a.opt.KeepFinishedMax {
		var oldest string
		for id, e := range a.finished {
			if oldest == "" || e.finishedAt.Before(a.finished[oldest].finishedAt) {
				oldest = id
			}
		}
		a.drop(oldest)
	}
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

	// The execution is built BEFORE the lock and reserved UNDER it, in the
	// same step as the check (#130). Checking, letting the lock go, setting
	// the step up and only then recording it let two starts of one id -- one
	// start sent twice -- both through: two processes under one id, and one
	// of them out of anybody's reach. Reserved first, a second start is
	// refused while the first is still being set up, and a resume or a cancel
	// arriving meanwhile finds it.
	e := &execution{id: req.ExecutionID, ring: a.opt.RingSize, first: 1, next: 1}
	e.cond = sync.NewCond(&e.mu)
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if req.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	e.stop = cancel

	a.mu.Lock()
	a.prune()
	if a.draining {
		a.mu.Unlock()
		cancel()
		return nil, ErrDraining
	}
	if _, running := a.runs[req.ExecutionID]; running {
		a.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("execution %q is already running here; a retry gets a new id",
			req.ExecutionID)
	}
	a.runs[req.ExecutionID] = e
	// A finished one is NOT refused, and it is not an oversight: an engine
	// older than #127 names an execution after its step -- the same id for
	// every run of the workflow -- so a finished id is how its next run
	// arrives. Refusing it failed every run of a host: step for --keep-finished
	// after the one before. The new execution replaces what was kept.
	a.drop(req.ExecutionID)
	a.mu.Unlock()

	// The file this step publishes through, on THIS host. Created before the
	// process so the variable can point at it, removed when the execution ends.
	output, err := os.CreateTemp("", "brevis-output-*.json")
	if err != nil {
		a.abandon(e)
		return nil, fmt.Errorf("this host could not make a file for the step's "+
			"published context: %w", err)
	}
	outputPath := output.Name()
	_ = output.Close()

	env, err := a.environment(req, outputPath)
	if err != nil {
		_ = os.Remove(outputPath)
		a.abandon(e)
		// Refused before spawning. The engine turns this into a failed step
		// naming the coordinate, which is the point: a command that starts with
		// VENDOR_TOKEN unset produces a 401 three layers down and blames the
		// wrong service.
		return nil, err
	}

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
		_ = os.Remove(outputPath)
		a.abandon(e)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = os.Remove(outputPath)
		a.abandon(e)
		return nil, err
	}
	// A cancel during the setup has already ended runCtx, and Start then
	// fails here -- the step is abandoned, never spawned.
	// Read BEFORE the fork, so the recorded second is never later than the
	// one the OS gives the process; startMatches relies on that.
	started := time.Now()
	if err := cmd.Start(); err != nil {
		_ = os.Remove(outputPath)
		a.abandon(e)
		return nil, fmt.Errorf("starting the step on this host: %w", err)
	}
	e.mu.Lock()
	e.cmd = cmd
	e.started = started
	e.mu.Unlock()
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
		e.mu.Lock()
		e.exited = true
		e.mu.Unlock()
		close(heartbeat)
		cancel()
		a.forget(req.ExecutionID)

		// BEFORE the exit line, because the engine stops reading at it: the
		// protocol says everything after KindExit is ignored, and published
		// context arriving there would be dropped without a word.
		a.publish(e, outputPath)
		_ = os.Remove(outputPath)

		e.emit(remote.Line{Kind: remote.KindExit, Code: exitCode(err)})
		e.close()

		// Only now, with the exit line in the ring, does the execution move
		// -- in one step, under one lock. It used to leave `runs` before the
		// exit line was written, and a resume in between (a connection
		// dropped at the very end) was refused: the engine failed a step that
		// had finished, its exit code and context still on their way.
		a.mu.Lock()
		delete(a.runs, req.ExecutionID)
		e.finishedAt = a.opt.Now()
		a.finished[req.ExecutionID] = e
		a.prune()
		a.mu.Unlock()

		// To disk, so a restart does not lose it (#137).
		a.persist(e)
	}()

	return a.reading(e, 0), nil
}

// abandon gives back the reservation of a step that never started: it leaves
// `runs` -- only if it is still this execution -- so a later start of the id is
// not refused forever, and it is closed, so a resume that found it during the
// setup ends instead of waiting on a ring nothing will write.
func (a *Agent) abandon(e *execution) {
	e.stop()
	a.mu.Lock()
	if a.runs[e.id] == e {
		delete(a.runs, e.id)
	}
	a.mu.Unlock()
	e.close()
	a.settle(e)
}

// publish turns what the step wrote into the marker the runner already reads.
//
// A MARKER AND NOT A NEW LINE KIND, which is the whole reason this is three
// lines. The `@brevis:` protocol on stdout is how a step publishes to the
// engine on every executor -- the runner recognises it, and recognising it
// there rather than in an executor is what the stages collector's own comment
// says gives the local executor the same for free. An agent that invented a
// `context` line would be a second road to the same place, for both programs
// to keep in agreement.
//
// Silent when there is nothing, because most steps publish nothing and an
// absent file is the normal case -- the same reading readPublished makes on
// the local executor.
func (a *Agent) publish(e *execution, path string) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	// Bounded, because this becomes a line in the log stream and a ring entry.
	// A step that writes a gigabyte there must not turn into a gigabyte on the
	// wire; it is told, rather than truncated into something that parses as
	// different data.
	if len(raw) > maxPublished {
		e.emit(remote.Line{Kind: remote.KindLog, Stream: "stderr", Message: fmt.Sprintf(
			"brevis: this step published %d bytes to BREVIS_OUTPUT and the limit is %d; "+
				"nothing was published. Publish what the steps below need, not the payload",
			len(raw), maxPublished)})
		return
	}
	// Validated here rather than sent on: a file that is not JSON cannot be
	// the `value` of a marker without making the whole line unparseable, and a
	// malformed line is the engine's problem for no reason.
	if !json.Valid(raw) {
		e.emit(remote.Line{Kind: remote.KindLog, Stream: "stderr", Message: fmt.Sprintf(
			"brevis: this step wrote %d bytes to BREVIS_OUTPUT that are not JSON; "+
				"nothing was published", len(raw))})
		return
	}
	line, err := json.Marshal(struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}{Type: "context", Value: json.RawMessage(raw)})
	if err != nil {
		return
	}
	e.emit(remote.Line{Kind: remote.KindLog, Stream: "stdout", Message: "@brevis:" + string(line)})
}

// maxPublished bounds what a step may hand back through the file.
//
// One mebibyte, which is the same order as the request limit this agent's own
// handler imposes. Context is what the steps below need to do their work --
// a path, a count, a watermark -- and a payload that size is a file in a
// bucket, not a variable.
const maxPublished = 1 << 20

// Resume serves the same execution from `after`.
//
// A gap is answered rather than papered over: when the ring no longer reaches
// back that far the engine is told what it wanted and what is available, and it
// fails the step. Serving from the oldest line instead would hand back a stream
// with a hole in the middle and no way to know how big.
func (a *Agent) Resume(execID string, after int64) (io.ReadCloser, error) {
	a.mu.Lock()
	a.prune()
	e := a.runs[execID]
	if e == nil {
		e = a.finished[execID]
	}
	a.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("execution %q is not running here; it finished longer ago "+
			"than this agent keeps, or this agent restarted and lost it: %w",
			execID, remote.ErrUnknownExecution)
	}

	e.mu.Lock()
	first := e.first
	e.mu.Unlock()
	if after+1 < first {
		return nil, remote.GapError{Wanted: after + 1, Available: first}
	}
	return a.reading(e, after), nil
}

// Cancel stops a step.
//
// The process GROUP is signalled, not the process: the shell spawned whatever
// the command is, and killing only the shell leaves it running.
func (a *Agent) Cancel(execID string) error {
	a.mu.Lock()
	a.prune()
	e := a.runs[execID]
	ended, done := a.finished[execID]
	a.mu.Unlock()

	// Cancelled is settled: whoever asked knows how it ends, and a drain does
	// not wait for an exit nobody is coming to read (#136).
	if done {
		a.settle(ended)
		// It finished: what was asked for is already true. Without this a
		// finished execution fell to the state file below and, with no
		// --state-dir, was answered "the process may still be running".
		return nil
	}
	if e == nil {
		// Not in memory: this agent restarted, and the file is what is left.
		return a.cancelFromState(execID)
	}
	e.stop()
	a.settle(e)
	// The command is read under the execution's lock, like `exited`: since
	// #130 an execution is visible from its reservation, before Start has
	// spawned anything and while it is writing e.cmd. Nil means not spawned
	// yet, and the stop above is then all it takes -- Start fails on the
	// ended context and abandons the step.
	e.mu.Lock()
	exited, cmd := e.exited, e.cmd
	e.mu.Unlock()
	if !exited && cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	return nil
}

// environment resolves the step's variables, secrets included.
//
// The ordering matters and matches the local executor's: the plain environment
// first, then the secrets, so a `secrets:` entry can deliberately override an
// ordinary variable of the same name. That is what "this one, on purpose"
// means.
func (a *Agent) environment(req remote.StartRequest, output string) ([]string, error) {
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

	// BREVIS_OUTPUT IS THIS HOST'S, overriding whatever arrived.
	//
	// The runner picks a path meaningful on the ENGINE's filesystem and sends
	// it with the rest of the environment. On a host the engine does not
	// manage that path is somebody else's, and a step that honours it writes a
	// file nobody reads -- silently, which is the failure mode the pod
	// executor's own override exists to prevent. It makes the same override
	// there, to the kubelet's termination message; this is that one, for a
	// filesystem the engine cannot see at all.
	//
	// Both halves together, as the pod does: the path the step writes to and
	// the path this agent reads back. Setting one without the other is the
	// silent failure spelled out.
	if output != "" {
		env[runcontext.EnvOutput] = output
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
