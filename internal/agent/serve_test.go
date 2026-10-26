package agent_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
)

// served is an agent on a real listener, run by Serve the way main runs it.
type served struct {
	url         string
	stop, force chan struct{}
	done        chan result
}

type result struct {
	cancelled int
	err       error
}

func serve(t *testing.T, drainTimeout time.Duration) *served {
	t.Helper()
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &served{
		url:   "http://" + ln.Addr().String(),
		stop:  make(chan struct{}),
		force: make(chan struct{}),
		done:  make(chan result, 1),
	}
	srv := &http.Server{Handler: a.Handler(quiet()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		n, err := a.Serve(srv, ln, s.stop, s.force, drainTimeout, quiet())
		s.done <- result{n, err}
	}()
	t.Cleanup(func() {
		select {
		case <-s.force:
		default:
			close(s.force)
		}
	})
	return s
}

// returned waits up to d for Serve to return.
func (s *served) returned(t *testing.T, d time.Duration) (result, bool) {
	t.Helper()
	select {
	case r := <-s.done:
		return r, true
	case <-time.After(d):
		return result{}, false
	}
}

// The issue itself: a signal while a step runs must not end the process --
// it ends when the step does and its exit has been read, and the listener
// stays up for that read.
func TestServeReturnsOnlyAfterTheRunningStepIsRead(t *testing.T) {
	s := serve(t, 0)
	resp := startOver(t, s.url, "s1", "sleep 0.5")

	close(s.stop)
	if _, ok := s.returned(t, 200*time.Millisecond); ok {
		t.Fatal("Serve returned with a step still running")
	}
	if code, ok := exitOf(lines(t, resp.Body)); !ok || code != 0 {
		t.Fatalf("the stream ended without exit 0 (exit %d, seen %v)", code, ok)
	}
	r, ok := s.returned(t, 5*time.Second)
	if !ok {
		t.Fatal("Serve did not return after the exit was read")
	}
	if r.err != nil || r.cancelled != 0 {
		t.Errorf("Serve = (%d, %v), wanted (0, nil)", r.cancelled, r.err)
	}
	if _, err := http.Get(s.url + "/health"); err == nil { //nolint:noctx,bodyclose
		t.Error("the listener is still up after Serve returned")
	}
}

func TestServeWithNothingRunningStopsAtOnce(t *testing.T) {
	s := serve(t, 0)
	close(s.stop)
	r, ok := s.returned(t, 2*time.Second)
	if !ok {
		t.Fatal("Serve did not return with nothing to drain")
	}
	if r.err != nil || r.cancelled != 0 {
		t.Errorf("Serve = (%d, %v), wanted (0, nil)", r.cancelled, r.err)
	}
}

// Past the timeout what is left is cancelled -- the whole group, as /cancel
// does, so nothing goes on running out of anybody's reach -- and said.
func TestADrainPastItsTimeoutCancelsTheWholeGroup(t *testing.T) {
	s := serve(t, 300*time.Millisecond)
	marker := filepath.Join(t.TempDir(), "still-alive")
	_ = startOver(t, s.url, "t1", fmt.Sprintf(`(sleep 1; touch %q) & sleep 30`, marker)).Body.Close()

	close(s.stop)
	r, ok := s.returned(t, 15*time.Second)
	if !ok {
		t.Fatal("Serve did not return after its drain timed out")
	}
	if r.err != nil || r.cancelled != 1 {
		t.Errorf("Serve = (%d, %v), wanted (1, nil)", r.cancelled, r.err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the child outlived the drain: only the shell was killed")
	}
}

// No timeout is no limit -- the orchestrator's grace period is the bound --
// and a second signal is how an operator says "now".
func TestASecondSignalEndsADrainThatHasNoLimit(t *testing.T) {
	s := serve(t, 0)
	_ = startOver(t, s.url, "f1", "sleep 30").Body.Close()

	close(s.stop)
	if _, ok := s.returned(t, 500*time.Millisecond); ok {
		t.Fatal("a drain with no limit gave up on its own")
	}
	close(s.force)
	r, ok := s.returned(t, 15*time.Second)
	if !ok {
		t.Fatal("Serve did not return after the second signal")
	}
	if r.err != nil || r.cancelled != 1 {
		t.Errorf("Serve = (%d, %v), wanted (1, nil)", r.cancelled, r.err)
	}
}
