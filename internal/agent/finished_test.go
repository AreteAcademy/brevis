package agent_test

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
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

// A second start of a finished id is a start sent twice -- a response lost on
// the way back -- and running the step again would be the double execution
// #121 ended. Refused, saying it already ran.
func TestAFinishedExecutionIsNotStartedAgain(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	finish(t, a, "done-3", "true")
	_, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "done-3", NodeID: "step", Command: "true",
	})
	if err == nil || !strings.Contains(err.Error(), "finished") {
		t.Errorf("a second start of a finished execution returned %v; wanted a refusal saying it finished", err)
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
