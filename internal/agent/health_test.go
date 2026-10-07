package agent_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AreteAcademy/brevis/internal/agent"
)

// A kubelet sends no Authorization header, ever.
//
// The agent's guard wraps the WHOLE mux -- which is the right default for a
// process whose other three routes run arbitrary commands -- so a health route
// added without an exemption answers 401 to every probe, and the pod it is
// supposed to gate never goes Ready.
func TestHealthAnswersAProbeCarryingNoToken(t *testing.T) {
	a := agent.New(agent.Options{Token: "right"})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health") //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health answered %d to a probe with no token", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not JSON: %q", raw)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, wanted ok", body.Status)
	}
}

// Pinned because Go 1.22+ matches by method and a pattern written without one
// answers every verb.
func TestTheWrongMethodDoesNotMatchHealth(t *testing.T) {
	a := agent.New(agent.Options{})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/health", "application/json", nil) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health answered %d, wanted 405", resp.StatusCode)
	}
}

// THE ONE THAT MATTERS: the exemption is one exact path and nothing else.
//
// Written as a prefix -- `strings.HasPrefix(path, "/health")` reads just as
// naturally -- it would stop being a list of exempt routes and start being a
// hole whose shape nobody states. The assertion that catches it is `/healthz`:
// under an exact match the guard refuses it 401 before the mux ever sees it,
// and under a prefix the mux answers 404, which also tells an unauthenticated
// caller that the path does not exist.
func TestTheExemptionIsOneExactPath(t *testing.T) {
	a := agent.New(agent.Options{Token: "right"})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	for _, c := range []struct {
		method, path, why string
	}{
		{http.MethodPost, "/v1/exec", "starting a step is the whole of what this guard protects"},
		{http.MethodPost, "/v1/exec/x/resume", "a resume reattaches to a running process"},
		{http.MethodPost, "/v1/exec/x/cancel", "a cancel signals a process group"},
		{http.MethodGet, "/healthz", "a prefix exemption would answer 404 and say the path is unknown"},
		{http.MethodGet, "/health/", "a trailing slash is a different path, not the same one"},
		{http.MethodGet, "/", "the root is not exempt either"},
	} {
		req, err := http.NewRequest(c.method, srv.URL+c.path, nil) //nolint:noctx
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d with no token, wanted 401: %s",
				c.method, c.path, resp.StatusCode, c.why)
		}
	}
}
