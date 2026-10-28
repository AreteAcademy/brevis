package agent_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// lines decodes a stream to its end.
func lines(t *testing.T, r io.Reader) []remote.Line {
	t.Helper()
	var out []remote.Line
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			continue
		}
		l, err := remote.Decode([]byte(raw))
		if err != nil {
			t.Fatalf("an undecodable line %q: %v", raw, err)
		}
		out = append(out, l)
	}
	return out
}

// finish starts a step and reads its first stream to the end, so the step has
// exited by the time this returns.
func finish(t *testing.T, a *agent.Agent, id, command string) []remote.Line {
	t.Helper()
	stream, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: id, NodeID: "step", Command: command,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	return lines(t, stream)
}

func exitOf(ls []remote.Line) (int, bool) {
	for _, l := range ls {
		if l.Kind == remote.KindExit {
			return l.Code, true
		}
	}
	return 0, false
}

// #125: a scheduler that was down while a long step ended has to be able to
// learn how it ended. The agent forgot an execution the moment its process
// exited -- "not running here; it finished" -- and the exit code, the context
// the step published and the end of its log went with it.
func TestAFinishedExecutionCanStillBeResumed(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	first := finish(t, a, "done-1",
		`echo working; echo '{"rows":7}' > "$BREVIS_OUTPUT"; exit 3`)
	if code, ok := exitOf(first); !ok || code != 3 {
		t.Fatalf("the first stream ended with exit %d (seen %v); wanted 3", code, ok)
	}

	again, err := a.Resume("done-1", 0)
	if err != nil {
		t.Fatalf("resuming a finished execution: %v", err)
	}
	defer func() { _ = again.Close() }()
	got := lines(t, again)

	if code, ok := exitOf(got); !ok || code != 3 {
		t.Errorf("the resumed stream ended with exit %d (seen %v); wanted 3", code, ok)
	}
	var published, output bool
	for _, l := range got {
		published = published || strings.Contains(l.Message, `@brevis:{"type":"context","value":{"rows":7}}`)
		output = output || l.Message == "working"
	}
	if !published || !output {
		t.Errorf("the resumed stream lost the log (%v) or the published context (%v)", output, published)
	}
}

// The released bug under it: the execution left the agent's map BEFORE its exit
// line was written, so a resume in that window -- a connection dropped at the
// very end -- was refused, and the engine failed a step that had finished.
// Resumed relentlessly while a step exits, it must never be refused.
func TestThereIsNoInstantAFinishingExecutionCannotBeResumed(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	stream, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "edge", NodeID: "step", Command: "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, stream) }()

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		r, err := a.Resume("edge", 0)
		if err != nil {
			t.Fatalf("a resume while the step was finishing was refused: %v", err)
		}
		_ = r.Close()
	}
}

// Cancelling what already finished asked for something already true. It was
// answered, without --state-dir, with an error saying the process "may still
// be running" -- of a process that had exited.
func TestCancellingAFinishedExecutionIsHonestlyANoOp(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	finish(t, a, "done-2", "true")
	if err := a.Cancel("done-2"); err != nil {
		t.Errorf("cancelling a finished execution: %v", err)
	}
}

// An engine older than #127 names an execution after its STEP -- "daily:extract"
// for every run of that workflow -- so a second start of a finished id is the
// next run, not a start sent twice. Refusing it, as this agent first did, failed
// every run of a host: step for an hour after the one before it, on every engine
// in the field. It runs, and replaces what was kept.
func TestAFinishedIdCanStartAgain(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	finish(t, a, "daily:extract", "exit 3")

	second := finish(t, a, "daily:extract", "exit 0")
	if code, ok := exitOf(second); !ok || code != 0 {
		t.Fatalf("the second start of a finished id ended %d (seen %v); wanted it run, exit 0", code, ok)
	}
	again, err := a.Resume("daily:extract", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if code, _ := exitOf(lines(t, again)); code != 0 {
		t.Errorf("a resume read the first execution's exit %d; the second one replaced it", code)
	}
}

// clock is a time the test moves by hand, so an hour of retention takes no
// hour.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// A finished execution is kept for --keep-finished, and not one moment past
// it: the set is memory, a ring per entry, and it has to stay bounded.
func TestAFinishedExecutionIsForgottenAfterItsRetention(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	a := agent.New(agent.Options{
		Shell: []string{"/bin/sh", "-c"}, KeepFinished: time.Hour, Now: c.Now,
	})
	finish(t, a, "kept", "true")

	c.advance(59 * time.Minute)
	if r, err := a.Resume("kept", 0); err != nil {
		t.Fatalf("within its hour the execution was already gone: %v", err)
	} else {
		_ = r.Close()
	}

	c.advance(2 * time.Minute)
	if _, err := a.Resume("kept", 0); err == nil || !strings.Contains(err.Error(), "not running here") {
		t.Errorf("past its hour the execution answered %v; wanted it forgotten", err)
	}
}

// And at most --keep-finished-max of them: past it, the OLDEST goes first --
// the one a returning scheduler is least likely to still be asking about.
func TestPastTheMaximumTheOldestFinishedExecutionGoesFirst(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	a := agent.New(agent.Options{
		Shell: []string{"/bin/sh", "-c"}, KeepFinished: time.Hour, KeepFinishedMax: 2, Now: c.Now,
	})
	for _, id := range []string{"oldest", "middle", "newest"} {
		finish(t, a, id, "true")
		c.advance(time.Minute)
	}

	if _, err := a.Resume("oldest", 0); err == nil {
		t.Error("the oldest of three finished executions survived a maximum of two")
	}
	for _, id := range []string{"middle", "newest"} {
		r, err := a.Resume(id, 0)
		if err != nil {
			t.Errorf("%s was evicted: %v", id, err)
			continue
		}
		_ = r.Close()
	}
}

// cutAtTheEnd is the agent's real HTTP client, with the network failing at the
// worst moment: the first stream breaks after its first line, and the resume
// only goes out once the step has already exited.
type cutAtTheEnd struct {
	remote.HTTPAgent
	exitedBy time.Duration
}

func (c cutAtTheEnd) Start(ctx context.Context, req remote.StartRequest) (remote.Session, error) {
	s, err := c.HTTPAgent.Start(ctx, req)
	if err != nil {
		return s, err
	}
	first, _ := bufio.NewReader(s.Stream).ReadBytes('\n')
	_ = s.Stream.Close()
	s.Stream = io.NopCloser(strings.NewReader(string(first)))
	return s, nil
}

func (c cutAtTheEnd) Resume(ctx context.Context, instance, execID string, r remote.Resume) (io.ReadCloser, error) {
	time.Sleep(c.exitedBy)
	return c.HTTPAgent.Resume(ctx, instance, execID, r)
}

// #125, through the engine: a connection that broke before the exit line, and
// came back only after the step had ended, used to fail the step -- "the stream
// broke and could not be resumed" -- whatever the step did. It now ends as the
// step ended, success or the exit code.
func TestAStepThatEndedWhileItsConnectionWasDownEndsAsItEnded(t *testing.T) {
	for _, c := range []struct {
		command string
		want    execution.EventKind
		code    int
	}{
		{"sleep 0.2; exit 0", execution.EventSucceeded, 0},
		{"sleep 0.2; exit 3", execution.EventFailed, 3},
	} {
		t.Run(c.command, func(t *testing.T) {
			a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
			srv := httptest.NewServer(a.Handler(quiet()))
			t.Cleanup(srv.Close)
			e := &remote.Executor{
				Host:       "test-host",
				Agent:      cutAtTheEnd{HTTPAgent: remote.HTTPAgent{BaseURL: srv.URL}, exitedBy: time.Second},
				LeaseLimit: 10 * time.Second,
			}

			events := run(t, e, task("ended-"+string(c.want), c.command))
			end := last(events)
			if end.Kind != c.want || end.ExitCode != c.code {
				t.Errorf("the step ended %s (code %d, %q); wanted %s with code %d",
					end.Kind, end.ExitCode, end.Message, c.want, c.code)
			}
		})
	}
}

// An execution this agent does not know is said apart on the wire -- 404, not
// a generic refusal -- because the engine does something different with it:
// a step whose agent lost it may start again with its own id (#125).
func TestAnUnknownExecutionIsSaidApart(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	_, err := remote.HTTPAgent{BaseURL: srv.URL}.Resume(context.Background(), "", "never-ran",
		remote.Resume{Protocol: remote.Protocol})
	if !errors.Is(err, remote.ErrUnknownExecution) {
		t.Errorf("resuming an execution this agent never had: %v; wanted ErrUnknownExecution", err)
	}
}

// A step that ended is over for the engine too, however many came before it on
// the same connection.
//
// The executor used to close a stream while its reader was still inside Read.
// Over a kept-alive connection that left a read in flight which, returning
// EOF later, took the transport's signal meant for the NEXT response on the
// same connection -- and that step, its exit already reported, never ended.
// Instant steps in a row make the window wide: a thousand of them hung in five
// runs out of six before the fix. A race, so a guard and not a proof.
func TestQuickStepsInARowAllEnd(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)
	e := &remote.Executor{Host: "one", Agent: remote.HTTPAgent{BaseURL: srv.URL}, LeaseLimit: 5 * time.Second}

	for i := range 1000 {
		if got := last(run(t, e, task(fmt.Sprintf("quick-%d", i), "true"))); got.Kind != execution.EventSucceeded {
			t.Fatalf("step %d ended %s: %v", i+1, got.Kind, got.Err)
		}
	}
}
