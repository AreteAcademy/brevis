package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
