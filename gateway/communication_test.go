package gateway_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

// unauthed is `authed` with no `listen.auth`: the endpoint nobody holds a key to.
var unauthed = strings.Replace(authed, "  auth: {type: bearer, keys_from: GW_KEYS}\n", "", 1)

// unsetCommunication leaves BREVIS_COMMUNICATION absent for the test, and
// restores whatever the shell had afterwards.
func unsetCommunication(t *testing.T) {
	t.Helper()
	t.Setenv("BREVIS_COMMUNICATION", "")
	if err := os.Unsetenv("BREVIS_COMMUNICATION"); err != nil {
		t.Fatal(err)
	}
}

func servingOpen(t *testing.T, yaml string) *httptest.Server {
	t.Helper()
	cfg, err := load(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := gateway.New(cfg, nil, everything()...)
	if err != nil {
		t.Fatal(err)
	}
	// Not t.Context(), for the reason servingAuthed gives.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postClick(t *testing.T, ts *httptest.Server, header string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/clicks", //nolint:noctx
		strings.NewReader(`{"event_id":"a","occurred_at":"2026-10-24T10:00:00Z"}`))
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// A gateway only the cluster reaches is integrated with by internal teams
// without anybody handing out keys: the deployment says so, and outside local
// an open endpoint starts.
func TestInternalOpensTheEndpointOutsideLocal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BREVIS_ENV", "prod")
	t.Setenv("BREVIS_COMMUNICATION", "internal")

	ts := servingOpen(t, fmtConfig(unauthed, dir))
	if code := postClick(t, ts, ""); code != http.StatusAccepted {
		t.Errorf("an internal gateway answered %d to a POST with no token", code)
	}
}

// External is the default: unset, it is today's rule, and the refusal names
// the other way out so the operator who meant internal finds it.
func TestExternalOrUnsetStillRefusesAnOpenEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BREVIS_ENV", "prod")

	for _, name := range []string{"unset", "external"} {
		t.Run(name, func(t *testing.T) {
			if name == "unset" {
				unsetCommunication(t)
			} else {
				t.Setenv("BREVIS_COMMUNICATION", "external")
			}
			_, err := load(t, fmtConfig(unauthed, dir))
			if err == nil {
				t.Fatal("an open endpoint was accepted outside local")
			}
			if !strings.Contains(err.Error(), "open relay") ||
				!strings.Contains(err.Error(), "BREVIS_COMMUNICATION=internal") {
				t.Errorf("the refusal is: %v", err)
			}
		})
	}
}

// Internal makes auth optional, not off: whoever wrote `bearer` believes the
// door is closed, and it is.
func TestInternalStillEnforcesADeclaredBearer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BREVIS_ENV", "prod")
	t.Setenv("BREVIS_COMMUNICATION", "internal")
	t.Setenv("GW_KEYS", "k")

	ts := servingOpen(t, fmtConfig(authed, dir))
	if code := postClick(t, ts, ""); code != http.StatusUnauthorized {
		t.Errorf("no token answered %d under internal with bearer declared", code)
	}
	if code := postClick(t, ts, "Bearer k"); code != http.StatusAccepted {
		t.Errorf("the right token answered %d", code)
	}
}

// A typo fails closed and loudly. Falling silently to external would hide the
// mistake; falling to internal would open a door nobody decided to open.
func TestAnUnknownCommunicationRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GW_KEYS", "k")

	for _, v := range []string{"interno", "Internal", "true"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("BREVIS_COMMUNICATION", v)
			// With auth declared, so nothing but the value can be the reason.
			_, err := load(t, fmtConfig(authed, dir))
			if err == nil {
				t.Fatalf("BREVIS_COMMUNICATION=%s was accepted", v)
			}
			if !strings.Contains(err.Error(), "internal") || !strings.Contains(err.Error(), "external") {
				t.Errorf("the refusal does not name the two values: %v", err)
			}
		})
	}
}

// Local is untouched: `docker compose up` with neither variable still starts
// open.
func TestLocalWithTheVariableUnsetIsStillOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BREVIS_ENV", "local")
	unsetCommunication(t)

	ts := servingOpen(t, fmtConfig(unauthed, dir))
	if code := postClick(t, ts, ""); code != http.StatusAccepted {
		t.Errorf("a local gateway answered %d to a POST with no token", code)
	}
}

// The door internal opened is said once at start, in the log of the
// deployment that opened it -- and only there: a gateway with a key, or a
// laptop, has no such door to mention.
func TestAnEndpointOpenedByInternalSaysSoAtStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GW_KEYS", "k")
	for _, c := range []struct {
		name, env, communication, yaml string
		says                           bool
	}{
		{"internal, no auth", "prod", "internal", unauthed, true},
		{"internal, bearer declared", "prod", "internal", authed, false},
		{"local, no auth", "local", "external", unauthed, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BREVIS_ENV", c.env)
			t.Setenv("BREVIS_COMMUNICATION", c.communication)
			var b bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			servingOpen(t, fmtConfig(c.yaml, dir))
			if said := strings.Contains(b.String(), "BREVIS_COMMUNICATION=internal"); said != c.says {
				t.Errorf("said the endpoint is open: %v, want %v; the log: %s", said, c.says, b.String())
			}
		})
	}
}
