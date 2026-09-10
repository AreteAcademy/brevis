package remote_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// fakeAgent is the host, and it exists BEFORE the real one on purpose.
//
// The contract has to be settled by tests before a second program exists to keep
// in sync with it: an agent written first would become the specification by
// accident, and the specification would then be whatever that program happened
// to do.
type fakeAgent struct {
	mu sync.Mutex

	// lines is everything the agent will ever emit, in order. `breakAfter`
	// closes the connection after that many of them, once per entry, so a test
	// can make the stream drop where it wants.
	lines      []string
	breakAfter []int

	// gapFrom makes Resume refuse: the ring no longer reaches that far.
	gapFrom int64

	// overlap makes Resume start this many lines EARLIER than asked, which is
	// what a real agent with a line-based ring does when its bookkeeping is
	// generous. The engine must not depend on the agent being precise.
	overlap int

	// resumeErr makes Resume fail outright.
	resumeErr error

	started   []remote.StartRequest
	resumes   []remote.Resume
	cancelled []string
	cancelErr error
	conns     int
}

func (f *fakeAgent) Start(_ context.Context, r remote.StartRequest) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, r)
	return f.open(0), nil
}

func (f *fakeAgent) Resume(_ context.Context, _ string, r remote.Resume) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, r)
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	if f.gapFrom > 0 && r.After < f.gapFrom {
		return nil, remote.GapError{Wanted: r.After + 1, Available: f.gapFrom}
	}
	from := int(r.After) - f.overlap
	if from < 0 {
		from = 0
	}
	return f.open(from), nil
}

func (f *fakeAgent) Cancel(_ context.Context, execID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, execID)
	return f.cancelErr
}

// open returns the lines after `from`, cut short if this connection is meant to
// break. Caller holds the lock.
func (f *fakeAgent) open(from int) io.ReadCloser {
	n := f.conns
	f.conns++

	rest := f.lines
	if from < len(rest) {
		rest = rest[from:]
	} else {
		rest = nil
	}
	if n < len(f.breakAfter) && f.breakAfter[n] >= 0 && f.breakAfter[n] < len(rest) {
		rest = rest[:f.breakAfter[n]]
	}
	return io.NopCloser(strings.NewReader(strings.Join(rest, "\n") + "\n"))
}

func (f *fakeAgent) request(i int) remote.StartRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[i]
}

func line(seq int, kind, stream, msg string) string {
	return fmt.Sprintf(`{"seq":%d,"kind":%q,"stream":%q,"message":%q}`, seq, kind, stream, msg)
}

func exitLine(seq, code int) string {
	return fmt.Sprintf(`{"seq":%d,"kind":"exit","code":%d}`, seq, code)
}

func task() execution.TaskExec {
	return execution.TaskExec{
		ExecutionID: "exec-1", NodeID: "extract", Workflow: "vendas",
		RunID: "run-1", Command: "python pipelines/orders.py",
	}
}

// collect drains the channel, failing the test rather than hanging forever.
func collect(t *testing.T, ch <-chan execution.Event) []execution.Event {
	t.Helper()
	var out []execution.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-deadline:
			t.Fatalf("the executor never closed its channel; got %d event(s) so far", len(out))
			return out
		}
	}
}

func run(t *testing.T, e *remote.Executor) []execution.Event {
	t.Helper()
	ch, err := e.Execute(context.Background(), task())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return collect(t, ch)
}

// The whole pipe, end to end: the agent's lines become the runner's events.
func TestTheAgentsStreamBecomesEvents(t *testing.T) {
	agent := &fakeAgent{lines: []string{
		`{"seq":1,"kind":"started"}`,
		line(2, "log", "stdout", "fetching page 1"),
		line(3, "log", "stderr", "a warning"),
		exitLine(4, 0),
	}}
	got := run(t, &remote.Executor{Agent: agent, Host: "dlt-runner-01"})

	if len(got) != 4 {
		t.Fatalf("got %d events: %+v", len(got), got)
	}
	if got[0].Kind != execution.EventStarted {
		t.Errorf("first event is %v", got[0].Kind)
	}
	if got[1].Message != "fetching page 1" || got[1].Stream != "stdout" {
		t.Errorf("log event: %+v", got[1])
	}
	// stderr is kept apart, which the POD executor cannot do at all -- the
	// kubelet merges the two streams. This executor is better informed there
	// and must not throw that away.
	if got[2].Stream != "stderr" {
		t.Errorf("stderr arrived as %q", got[2].Stream)
	}
	if got[3].Kind != execution.EventSucceeded {
		t.Errorf("last event is %v", got[3].Kind)
	}
}

// A non-zero exit is a failure carrying the code, not a bare error.
func TestANonZeroExitCarriesItsCode(t *testing.T) {
	agent := &fakeAgent{lines: []string{`{"seq":1,"kind":"started"}`, exitLine(2, 3)}}
	got := run(t, &remote.Executor{Agent: agent, Host: "box"})

	last := got[len(got)-1]
	if last.Kind != execution.EventFailed || last.ExitCode != 3 {
		t.Errorf("last event: %+v", last)
	}
}

// DECISION 3, and the one this executor must never get wrong.
//
// The engine has never transported a secret value: kubernetes emits a
// secretKeyRef for the kubelet to resolve, local passes along variables already
// in its own environment. This asserts the coordinates cross and the values do
// not -- because the alternative was "the engine resolves and sends over mTLS",
// which protects the wire and does nothing about the dispatcher, the assembly
// log, or any TaskExec dump.
func TestOnlyTheCoordinateCrossesTheWireAndNeverTheValue(t *testing.T) {
	agent := &fakeAgent{lines: []string{exitLine(1, 0)}}
	e := &remote.Executor{Agent: agent, Host: "box"}

	tk := task()
	tk.Secrets = map[string]string{"VENDOR_TOKEN": "vendor-api/token"}
	tk.Env = map[string]string{"STAGE": "prod"}
	// The value exists in THIS process, which is exactly the trap: an executor
	// that resolved locally would find it here and send it.
	t.Setenv("VENDOR_TOKEN", "s3cr3t-do-not-send")

	ch, err := e.Execute(context.Background(), tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	collect(t, ch)

	sent := agent.request(0)
	if sent.Secrets["VENDOR_TOKEN"] != "vendor-api/token" {
		t.Errorf("the coordinate did not cross: %v", sent.Secrets)
	}
	if strings.Contains(fmt.Sprint(sent), "s3cr3t-do-not-send") {
		t.Fatalf("THE SECRET VALUE CROSSED THE WIRE: %+v", sent)
	}
	if sent.Env["STAGE"] != "prod" {
		t.Errorf("ordinary env did not cross: %v", sent.Env)
	}
}

// DECISION 1. A dropped connection resumes from the last line seen, and the
// engine tells the agent where it stopped.
func TestADroppedConnectionResumesWhereItStopped(t *testing.T) {
	agent := &fakeAgent{
		lines: []string{
			`{"seq":1,"kind":"started"}`,
			line(2, "log", "stdout", "one"),
			line(3, "log", "stdout", "two"),
			exitLine(4, 0),
		},
		breakAfter: []int{2}, // the first connection dies after two lines
	}
	got := run(t, &remote.Executor{Agent: agent, Host: "box"})

	if len(agent.resumes) != 1 {
		t.Fatalf("resumed %d time(s)", len(agent.resumes))
	}
	if agent.resumes[0].After != 2 {
		t.Errorf("resumed after line %d, wanted 2", agent.resumes[0].After)
	}
	if got[len(got)-1].Kind != execution.EventSucceeded {
		t.Errorf("a resumable break failed the step: %+v", got)
	}
	var logs []string
	for _, e := range got {
		if e.Kind == execution.EventLog {
			logs = append(logs, e.Message)
		}
	}
	if strings.Join(logs, ",") != "one,two" {
		t.Errorf("logs = %v", logs)
	}
}

// And the reason the sequence number exists at all.
//
// Log lines, phases, published context and gauges all survive being replayed.
// A COUNTER metric does not: the engine's registry does `Add`, so a replayed
// line adds twice and the metric reads high while nothing fails. An overlapping
// resume must not emit the same line again.
func TestAnOverlappingResumeDoesNotReplayALine(t *testing.T) {
	agent := &fakeAgent{
		lines: []string{
			`{"seq":1,"kind":"started"}`,
			line(2, "log", "stdout", `@brevis:{"type":"metric","name":"rows","kind":"counter","value":1}`),
			line(3, "log", "stdout", "after"),
			exitLine(4, 0),
		},
		breakAfter: []int{2},
		// The agent replays generously: its resume starts one line EARLIER than
		// asked. A precise agent would not, and the engine must not depend on
		// the agent being precise -- it is a separate program on a separate
		// release cycle, on a host this engine does not administer.
		overlap: 1,
	}
	e := &remote.Executor{Agent: agent, Host: "box"}
	ch, err := e.Execute(context.Background(), task())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := collect(t, ch)

	var counters int
	for _, ev := range got {
		if strings.Contains(ev.Message, `"kind":"counter"`) {
			counters++
		}
	}
	if counters != 1 {
		t.Errorf("the counter line was emitted %d times; a replay adds it twice in the registry", counters)
	}
}

// A gap is TERMINAL, and that is the decision rather than an accident.
//
// A step whose output is missing an unknown number of lines is worse than a step
// that failed: its counters are wrong by an unknown amount and nothing says so.
func TestAGapFailsTheStepRatherThanContinuingWithAHole(t *testing.T) {
	agent := &fakeAgent{
		lines: []string{
			`{"seq":1,"kind":"started"}`,
			line(2, "log", "stdout", "one"),
			exitLine(3, 0),
		},
		breakAfter: []int{1},
		gapFrom:    9, // the ring has moved well past line 2
	}
	got := run(t, &remote.Executor{Agent: agent, Host: "box"})

	last := got[len(got)-1]
	if last.Kind != execution.EventFailed {
		t.Fatalf("a gap did not fail the step: %+v", got)
	}
	for _, want := range []string{"no longer replay", "counters"} {
		if !strings.Contains(last.Message, want) {
			t.Errorf("the message does not explain the gap (%q): %s", want, last.Message)
		}
	}
}

// The retries are bounded. An agent that drops the connection every second is
// not a network blip, and retrying forever turns a broken host into a step that
// never ends.
func TestAStreamThatKeepsBreakingIsGivenUpOn(t *testing.T) {
	agent := &fakeAgent{
		lines: []string{`{"seq":1,"kind":"started"}`, exitLine(2, 0)},
		// The first connection delivers one line and dies; every one after it
		// delivers nothing and dies, which is what a host in trouble looks
		// like -- it accepts the connection and produces no work.
		breakAfter: []int{1, 0, 0, 0, 0, 0, 0, 0},
	}
	got := run(t, &remote.Executor{Agent: agent, Host: "box", Retries: 2})

	last := got[len(got)-1]
	if last.Kind != execution.EventFailed {
		t.Fatalf("an endlessly broken stream did not fail: %+v", got)
	}
	if !strings.Contains(last.Message, "may still be running") {
		t.Errorf("the message hides that the process may survive: %s", last.Message)
	}
	// Retries=2 buys exactly two resumes, and then the step is given up on.
	if len(agent.resumes) != 2 {
		t.Errorf("resumed %d times with Retries=2", len(agent.resumes))
	}
}

// DECISION 4. Silence longer than the lease fails the step, and the message says
// what is NOT known.
func TestAnAgentThatGoesQuietLosesItsLease(t *testing.T) {
	// A stream that opens and then never says anything, and never closes.
	agent := &quietAgent{}
	e := &remote.Executor{Agent: agent, Host: "box", LeaseLimit: 60 * time.Millisecond}

	got := run(t, e)
	last := got[len(got)-1]
	if last.Kind != execution.EventFailed {
		t.Fatalf("a silent agent did not lose its lease: %+v", got)
	}
	// The actionable half: the engine gave up, and the process may well still
	// be running on a host it cannot see.
	if !strings.Contains(last.Message, "MAY STILL BE RUNNING") {
		t.Errorf("the message does not say the process may survive: %s", last.Message)
	}
}

// ANY line renews the lease, not only `alive`.
//
// A step producing output is plainly alive, and requiring the heartbeat on top
// would fail a healthy chatty step whose agent was busy writing its log. The
// code says so; this is what makes it true.
func TestOutputAloneKeepsTheLeaseWithNoHeartbeat(t *testing.T) {
	const lease = 60 * time.Millisecond
	agent := &pacedAgent{gap: lease / 2, lines: []string{
		`{"seq":1,"kind":"started"}`,
		line(2, "log", "stdout", "page 1"),
		line(3, "log", "stdout", "page 2"),
		line(4, "log", "stdout", "page 3"),
		line(5, "log", "stdout", "page 4"),
		exitLine(6, 0),
	}}
	got := run(t, &remote.Executor{Agent: agent, Host: "box", LeaseLimit: lease})

	if got[len(got)-1].Kind != execution.EventSucceeded {
		t.Fatalf("a chatty step with no heartbeat lost its lease: %+v", got)
	}
}

// An agent that dies AFTER speaking loses its lease too.
//
// The obvious test is an agent that never says anything, and it is the weaker
// one: the lease has not been renewed yet, so a timer armed once at the start
// catches it. This is the realistic failure -- the host answers, the step runs
// for a while, and then the machine goes away -- and it is what proves the lease
// keeps expiring rather than being armed once.
//
// Found by mutation: stopping the timer without re-arming it passed every test
// there was, while leaving a step to hang forever.
func TestAnAgentThatDiesAfterSpeakingAlsoLosesItsLease(t *testing.T) {
	const lease = 60 * time.Millisecond
	agent := &pacedAgent{
		gap: lease / 4,
		lines: []string{
			`{"seq":1,"kind":"started"}`,
			line(2, "log", "stdout", "fetching page 1"),
		},
		thenSilent: true,
	}
	got := run(t, &remote.Executor{Agent: agent, Host: "box", LeaseLimit: lease})

	last := got[len(got)-1]
	if last.Kind != execution.EventFailed {
		t.Fatalf("an agent that died after speaking did not lose its lease: %+v", got)
	}
	if !strings.Contains(last.Message, "MAY STILL BE RUNNING") {
		t.Errorf("the message does not say the process may survive: %s", last.Message)
	}
}

// A step that is merely QUIET keeps its lease, because the agent renews it.
// That is what makes silence mean death rather than slowness, and it is the
// whole reason `alive` exists.
//
// The lines are PACED, and that is the test rather than a detail: an earlier
// version sent three alive lines instantly under a one-second lease, which
// would have passed with no renewal logic at all. Here the step lives four
// times its own lease and only the renewals carry it.
func TestAQuietStepKeepsItsLeaseThroughAliveLines(t *testing.T) {
	const lease = 60 * time.Millisecond
	agent := &pacedAgent{gap: lease / 2, lines: []string{
		`{"seq":1,"kind":"started"}`,
		`{"seq":2,"kind":"alive"}`,
		`{"seq":3,"kind":"alive"}`,
		`{"seq":4,"kind":"alive"}`,
		`{"seq":5,"kind":"alive"}`,
		`{"seq":6,"kind":"alive"}`,
		exitLine(7, 0),
	}}
	got := run(t, &remote.Executor{Agent: agent, Host: "box", LeaseLimit: lease})

	if got[len(got)-1].Kind != execution.EventSucceeded {
		t.Fatalf("a quiet-but-alive step failed: %+v", got)
	}
	// And the heartbeat is not something a person needs in a log.
	for _, e := range got {
		if e.Kind == execution.EventLog {
			t.Errorf("an alive line reached the log: %+v", e)
		}
	}
}

// pacedAgent writes its lines slowly, so the lease is exercised in time rather
// than all at once.
type pacedAgent struct {
	lines []string
	gap   time.Duration

	// thenSilent leaves the connection OPEN and mute after the lines, which is
	// what an agent whose host died mid-run looks like from here. It is not the
	// same as an agent that never spoke: the lease has been renewed by then,
	// and what is under test is whether it goes on expiring afterwards.
	thenSilent bool
}

func (p *pacedAgent) Start(ctx context.Context, _ remote.StartRequest) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		for _, l := range p.lines {
			select {
			case <-time.After(p.gap):
			case <-ctx.Done():
				_ = pw.CloseWithError(ctx.Err())
				return
			}
			if _, err := io.WriteString(pw, l+"\n"); err != nil {
				return
			}
		}
		if p.thenSilent {
			<-ctx.Done()
			_ = pw.CloseWithError(ctx.Err())
			return
		}
		_ = pw.Close()
	}()
	return pr, nil
}

func (p *pacedAgent) Resume(context.Context, string, remote.Resume) (io.ReadCloser, error) {
	return nil, errors.New("the paced agent is not meant to be resumed")
}

func (p *pacedAgent) Cancel(context.Context, string) error { return nil }

// DECISION 2. Cancel reaches the agent, and says plainly when it could not.
func TestCancelReachesTheAgent(t *testing.T) {
	agent := &fakeAgent{lines: []string{exitLine(1, 0)}}
	e := &remote.Executor{Agent: agent, Host: "box"}
	if err := e.Cancel(context.Background(), "exec-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(agent.cancelled) != 1 || agent.cancelled[0] != "exec-1" {
		t.Errorf("cancelled = %v", agent.cancelled)
	}
}

// An unreachable agent is a REAL STATE and the error names it: the step is
// cancelled here and may still be running there. A spinner that never resolves
// is what this replaces.
func TestAnUnreachableAgentSaysTheProcessMaySurvive(t *testing.T) {
	agent := &fakeAgent{cancelErr: errors.New("connection refused")}
	e := &remote.Executor{Agent: agent, Host: "dlt-runner-01"}

	err := e.Cancel(context.Background(), "exec-1")
	if err == nil {
		t.Fatal("an unreachable agent reported a clean cancel")
	}
	for _, want := range []string{"dlt-runner-01", "may still be running"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

// A line this engine does not understand ends the stream rather than being
// ignored. The agent is a separate program on a separate release cycle, and a
// bad deploy over there must not become a step that quietly loses half its
// output over here.
func TestAnUnknownLineIsRefusedRatherThanSkipped(t *testing.T) {
	agent := &fakeAgent{lines: []string{
		`{"seq":1,"kind":"started"}`,
		`{"seq":2,"kind":"teleport"}`,
		exitLine(3, 0),
	}, gapFrom: 99}
	got := run(t, &remote.Executor{Agent: agent, Host: "box", Retries: 0})

	if got[len(got)-1].Kind != execution.EventFailed {
		t.Errorf("an unknown kind was skipped: %+v", got)
	}
}

// `host:` and `image:` are alternatives, and naming both is refused before
// anything runs rather than resolved by precedence nobody would remember.
func TestAHostAndAnImageTogetherAreRefused(t *testing.T) {
	e := &remote.Executor{Agent: &fakeAgent{}, Host: "box"}
	tk := task()
	tk.Image = "python:3.12"
	if _, err := e.Execute(context.Background(), tk); err == nil {
		t.Fatal("a task with both a host and an image was accepted")
	}
}

// quietAgent opens a stream that never produces a line and never ends, which is
// what an agent whose host died looks like from here.
type quietAgent struct{ cancelled bool }

func (q *quietAgent) Start(ctx context.Context, _ remote.StartRequest) (io.ReadCloser, error) {
	return silence{ctx: ctx}, nil
}

func (q *quietAgent) Resume(ctx context.Context, _ string, _ remote.Resume) (io.ReadCloser, error) {
	return silence{ctx: ctx}, nil
}

func (q *quietAgent) Cancel(context.Context, string) error { q.cancelled = true; return nil }

type silence struct{ ctx context.Context }

func (s silence) Read([]byte) (int, error) { <-s.ctx.Done(); return 0, io.EOF }
func (s silence) Close() error             { return nil }
