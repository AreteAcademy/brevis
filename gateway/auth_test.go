package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
)

const authed = `
name: g
listen:
  auth: {type: bearer, keys_from: GW_KEYS}
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 1, every: 1h}}
    sink: {type: files, path: %s/out/}
    dead_letter: {type: files, path: %s/dead/}
`

// Without a key nothing is accepted, and with the right one it is. An
// ingestion endpoint is a write endpoint on somebody's topic: an
// unauthenticated one on a routable address is not a gateway with a gap, it is
// an open relay.
func TestBearerKeysDecideWhoMayWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GW_KEYS", "key-one, key-two")
	ts := servingAuthed(t, dir)
	defer ts.Close()

	body := `{"event_id":"a","occurred_at":"2026-09-24T10:00:00Z"}`

	for _, c := range []struct {
		name, header string
		want         int
	}{
		{"no header at all", "", http.StatusUnauthorized},
		{"a key nobody issued", "Bearer nope", http.StatusUnauthorized},
		{"the right key without the scheme", "key-one", http.StatusUnauthorized},
		{"the first key", "Bearer key-one", http.StatusAccepted},
		// Every key in the list works, not just the first: rotation is adding
		// the new one, moving the callers, then removing the old.
		{"the second key", "Bearer key-two", http.StatusAccepted},
		// Whitespace is trimmed on BOTH sides -- the key list and the header --
		// so a stray space from a copy-paste works. It cannot turn a wrong key
		// into a right one; it only makes " k " and "k" the same, and a bearer
		// token with meaningful whitespace is not a thing anybody issues.
		{"a stray space after the scheme", "Bearer  key-two", http.StatusAccepted},
	} {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/clicks", strings.NewReader(body)) //nolint:noctx
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != c.want {
				t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
			}
		})
	}
}

// A readiness probe carries no credential, and one that needed a token would
// report the gateway down whenever the token was wrong -- a different outage
// from the one it exists to see.
func TestHealthIsOutsideTheGuard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GW_KEYS", "k")
	ts := servingAuthed(t, dir)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health") //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health answered %d to a probe with no token", resp.StatusCode)
	}
}

// A path that is not a stream answers 401 too. Otherwise an unauthenticated
// caller learns which paths exist by reading the status codes.
func TestAnUnknownPathDoesNotLeakThatItIsUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GW_KEYS", "k")
	ts := servingAuthed(t, dir)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/does-not-exist", "application/json", strings.NewReader("{}")) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unknown path answered %d, which tells a stranger it is unknown",
			resp.StatusCode)
	}
}

// Outside local the gateway refuses to start without auth -- the same rule the
// engine follows, spelled with the same word, so an operator learns it once.
func TestOutsideLocalAnOpenEndpointIsRefused(t *testing.T) {
	dir := t.TempDir()
	open := strings.Replace(authed, "  auth: {type: bearer, keys_from: GW_KEYS}\n", "", 1)

	t.Setenv("BREVIS_ENV", "prod")
	_, err := load(t, fmtConfig(open, dir))
	if err == nil {
		t.Fatal("an open endpoint was accepted outside local")
	}
	if !strings.Contains(err.Error(), "open relay") {
		t.Errorf("the refusal is: %v", err)
	}

	// And locally it is allowed, because asking for a token on every
	// `docker compose up` only teaches a team to turn authentication off.
	t.Setenv("BREVIS_ENV", "local")
	if _, err := load(t, fmtConfig(open, dir)); err != nil {
		t.Errorf("locally an open endpoint was refused: %v", err)
	}
}

// Half configured is worse than nothing: whoever wrote `bearer` believes the
// door is closed.
func TestAuthWithNoKeysIsRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BREVIS_ENV", "local")

	_, err := load(t, fmtConfig(strings.Replace(authed,
		"{type: bearer, keys_from: GW_KEYS}", "{type: bearer}", 1), dir))
	if err == nil || !strings.Contains(err.Error(), "believes the door is closed") {
		t.Errorf("a bearer with no keys_from: %v", err)
	}

	// And a keys_from naming an empty variable fails at START, not per request:
	// a gateway that accepts nothing should say so before it is deployed.
	t.Setenv("GW_KEYS", "")
	cfg, err := load(t, fmtConfig(authed, dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.New(cfg, nil); err == nil {
		t.Error("it started with a key list that accepts nothing")
	}
}

func fmtConfig(tpl, dir string) string {
	return strings.ReplaceAll(tpl, "%s", dir)
}

func servingAuthed(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	path := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(path, []byte(fmtConfig(authed, dir)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := gateway.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(t.Context()) })
	return httptest.NewServer(srv.Handler())
}
