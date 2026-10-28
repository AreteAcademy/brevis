package agent_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// post sends one JSON request to the agent and returns the response.
func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw)) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// A draining agent takes no new step (#136).
//
// 503 and not 400, because nothing about the request is wrong: another agent,
// or this one's replacement, would run it. And the id is NOT reserved -- a
// refusal that left it in `runs` would hold the drain open on a step that
// never existed.
func TestADrainingAgentRefusesAStartAndReservesNothing(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	a.Drain()

	resp := post(t, srv.URL+"/v1/exec", remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "d1", NodeID: "step", Command: "true",
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a start on a draining agent answered %d, wanted 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, wanted 1", got)
	}
	if _, err := a.Resume("d1", 0); !errors.Is(err, remote.ErrUnknownExecution) {
		t.Errorf("the refused id is known to the agent: %v", err)
	}
}

// The refusal names itself, and closes the connection (#146).
//
// A 503 alone is also what a balancer with nobody behind it answers, and the
// engine treats the two differently: a draining agent has a replica that
// would run the step. And the connection is closed so that no client -- the
// engine's keep-alive above all -- sends the next start down the same one, to
// the same agent, which is how a pool with a healthy replica failed ten runs.
func TestADrainingRefusalSaysSoAndClosesTheConnection(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	a.Drain()

	resp := post(t, srv.URL+"/v1/exec", remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "d1", NodeID: "step", Command: "true",
	})
	if got := resp.Header.Get(remote.HeaderRefusal); got != remote.RefusalDraining {
		t.Errorf("%s = %q, wanted %q", remote.HeaderRefusal, got, remote.RefusalDraining)
	}
	if !resp.Close {
		t.Error("the connection was left open for the next start")
	}
}

// Only the draining refusal says it: one about the request is not a reason to
// try another agent, and leaves the connection alone.
func TestAnotherRefusalIsNotMarkedDraining(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	resp := post(t, srv.URL+"/v1/exec", remote.StartRequest{
		Protocol: remote.Protocol + 1, ExecutionID: "d1", NodeID: "step", Command: "true",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a start in another protocol answered %d, wanted 400", resp.StatusCode)
	}
	if got := resp.Header.Get(remote.HeaderRefusal); got != "" {
		t.Errorf("%s = %q on a 400, wanted none", remote.HeaderRefusal, got)
	}
	if resp.Close {
		t.Error("a 400 closed the connection")
	}
}

// The probe says so too, still without a token: a load balancer in front of a
// pool stops sending starts here, which is cheaper than each one being refused.
func TestHealthSaysDrainingToAProbeCarryingNoToken(t *testing.T) {
	a := agent.New(agent.Options{Token: "right"})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	a.Drain()

	resp, err := http.Get(srv.URL + "/health") //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/health on a draining agent answered %d, wanted 503", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not JSON: %q", raw)
	}
	if body.Status != "draining" {
		t.Errorf("status = %q, wanted draining", body.Status)
	}
}

// What a drain is FOR: the engine that restarted in the same rollout comes back
// for the step it handed over, and finds it. A drain that refused the resume
// would wait for a step nobody can read.
func TestADrainingAgentStillResumesAndCancels(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	for _, id := range []string{"r1", "c1"} {
		stream, err := a.Start(context.Background(), remote.StartRequest{
			Protocol: remote.Protocol, ExecutionID: id, NodeID: "step", Command: "sleep 1",
		})
		if err != nil {
			t.Fatal(err)
		}
		// The engine went away: nobody is reading.
		_ = stream.Close()
	}

	a.Drain()

	resp := post(t, srv.URL+"/v1/exec/r1/resume", remote.Resume{Protocol: remote.Protocol})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a resume on a draining agent answered %d, wanted 200", resp.StatusCode)
	}
	if code, ok := exitOf(lines(t, resp.Body)); !ok || code != 0 {
		t.Errorf("the resumed stream ended without exit 0 (exit %d, seen %v)", code, ok)
	}

	resp = post(t, srv.URL+"/v1/exec/c1/cancel", struct {
		Protocol int `json:"protocol"`
	}{remote.Protocol})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a cancel on a draining agent answered %d, wanted 200", resp.StatusCode)
	}
}

// startOver starts a step the way the engine does, over HTTP, and returns the
// response with its stream unread.
func startOver(t *testing.T, url, id, command string) *http.Response {
	t.Helper()
	resp := post(t, url+"/v1/exec", remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: id, NodeID: "step", Command: command,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start %s answered %d", id, resp.StatusCode)
	}
	return resp
}

func waitFor(a *agent.Agent, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return a.Wait(ctx)
}

// The drain waits for the STEP, not for a connection: it ends when the exit
// line has gone out on a stream, and not a moment before.
func TestADrainWaitsUntilTheExitHasBeenRead(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	resp := startOver(t, srv.URL, "w1", "sleep 1")
	a.Drain()

	err := waitFor(a, 200*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait returned %v while the step was running", err)
	}
	if !strings.Contains(err.Error(), "w1") {
		t.Errorf("the error does not name what it waits for: %v", err)
	}

	if code, ok := exitOf(lines(t, resp.Body)); !ok || code != 0 {
		t.Fatalf("the stream ended without exit 0 (exit %d, seen %v)", code, ok)
	}
	if err := waitFor(a, 5*time.Second); err != nil {
		t.Fatalf("Wait after the exit was read: %v", err)
	}
}

// The case the issue's rollout hit: the engine went away, the step ended with
// nobody reading. Leaving then would lose its exit, and the engine coming back
// would be answered 404 -- and start the step AGAIN under the same id (#125).
func TestAStepThatEndsUnreadHoldsTheDrainUntilAResumeReadsIt(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	_ = startOver(t, srv.URL, "u1", "sleep 0.2").Body.Close()
	a.Drain()

	time.Sleep(600 * time.Millisecond) // the step has ended, unread
	if err := waitFor(a, 100*time.Millisecond); err == nil {
		t.Fatal("the drain ended with an exit nobody had read")
	}

	resp := post(t, srv.URL+"/v1/exec/u1/resume", remote.Resume{Protocol: remote.Protocol})
	if _, ok := exitOf(lines(t, resp.Body)); !ok {
		t.Fatal("the resume did not carry the exit")
	}
	if err := waitFor(a, 5*time.Second); err != nil {
		t.Fatalf("Wait after the resume read the exit: %v", err)
	}
}

// Reading SOME of the stream is not reading the end of it.
func TestAResumeCutBeforeTheExitDoesNotEndTheDrain(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	_ = startOver(t, srv.URL, "x1", "echo one; sleep 0.3").Body.Close()
	a.Drain()

	resp := post(t, srv.URL+"/v1/exec/x1/resume", remote.Resume{Protocol: remote.Protocol})
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close() // cut after one line

	time.Sleep(700 * time.Millisecond)
	if err := waitFor(a, 100*time.Millisecond); err == nil {
		t.Fatal("a stream cut before the exit ended the drain")
	}
}

// Whoever cancelled already knows how it ends; nobody is coming to read it.
func TestACancelledStepDoesNotHoldTheDrain(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	_ = startOver(t, srv.URL, "k1", "sleep 30").Body.Close()
	// Long enough for the server to see the hang-up. Sooner, the exit the cancel
	// causes can still go out on the first stream -- into a socket buffer, which
	// counts as read -- and this test would pass without the cancel settling
	// anything.
	time.Sleep(200 * time.Millisecond)
	a.Drain()

	resp := post(t, srv.URL+"/v1/exec/k1/cancel", struct {
		Protocol int `json:"protocol"`
	}{remote.Protocol})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel answered %d", resp.StatusCode)
	}
	if err := waitFor(a, 5*time.Second); err != nil {
		t.Fatalf("a cancelled step held the drain: %v", err)
	}
}

// The drain cannot let a start through and then not wait for it.
//
// Starts race the flip; half ask for a secret this host does not serve, so
// they are reserved and then abandoned. Afterwards every start answered 200 is
// waited for, every one refused is not, and nothing else is.
func TestNoStartSlipsPastTheDrain(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	var (
		mu       sync.Mutex
		accepted = map[string]bool{}
		wg       sync.WaitGroup
		ready    = make(chan struct{})
	)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("race-%d", i)
			req := remote.StartRequest{
				Protocol: remote.Protocol, ExecutionID: id, NodeID: "step", Command: "sleep 2",
			}
			if i%2 == 1 {
				req.Secrets = map[string]string{"X": "not-served/key"}
			}
			raw, _ := json.Marshal(req)
			<-ready
			resp, err := http.Post(srv.URL+"/v1/exec", "application/json", bytes.NewReader(raw)) //nolint:noctx
			if err != nil {
				t.Error(err)
				return
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			switch resp.StatusCode {
			case http.StatusOK:
				mu.Lock()
				accepted[id] = true
				mu.Unlock()
			case http.StatusServiceUnavailable, http.StatusBadRequest:
			default:
				t.Errorf("start %s answered %d", id, resp.StatusCode)
			}
		}()
	}
	close(ready)
	time.Sleep(5 * time.Millisecond)
	a.Drain()
	wg.Wait()

	pending := map[string]bool{}
	for _, id := range a.Pending() {
		pending[id] = true
	}
	for id := range accepted {
		if !pending[id] {
			t.Errorf("%s was started and the drain is not waiting for it", id)
		}
	}
	for id := range pending {
		if !accepted[id] {
			t.Errorf("the drain waits for %s, which never started", id)
		}
	}
}
