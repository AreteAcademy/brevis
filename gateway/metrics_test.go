package gateway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
)

// A series nobody incremented is absent, not zero.
//
// Prometheus handles an absent series; what it cannot do is tell a real zero
// from a placeholder, and "no batch was ever buried" and "burying is at zero
// because I wrote it that way" are different facts.
func TestAnUntouchedSeriesIsAbsent(t *testing.T) {
	m := gateway.NewMetrics()
	if text := render(t, m); strings.Contains(text, "brevis_gateway") {
		t.Errorf("a fresh gateway reported series it never touched:\n%s", text)
	}
}

// The whole path, end to end: post, deliver, scrape, and read the numbers back.
func TestTheNumbersComeOutOfARealRequest(t *testing.T) {
	sink := &recordingSink{}
	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("e-1", "a.b"), http.StatusAccepted)
	post(t, ts.URL+"/v1/clicks", event("e-2", "a.b"), http.StatusAccepted)
	// One that cannot be identified: no source_key.
	post(t, ts.URL+"/v1/clicks", `{"occurred_at":"2026-09-24T10:00:00Z"}`, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	text := render(t, srv.Metrics())
	for _, want := range []string{
		`brevis_gateway_events_received_total{stream="clicks",format="json"} 2`,
		`brevis_gateway_events_rejected_total{stream="clicks",reason="no_source_key"} 1`,
		`brevis_gateway_batches_total{stream="clicks",sink="a recording sink",outcome="delivered"} 2`,
		`brevis_gateway_buffer_records{stream="clicks"} 0`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// The exposition is NEVER on the ingest mux.
//
// This is the rule that matters most here and least in the engine: the
// gateway's port is public by design -- it is where clients POST -- so a
// /metrics on it would publish every stream name, path and destination to
// whoever finds the path.
func TestMetricsAreNotOnTheIngestPort(t *testing.T) {
	sink := &recordingSink{}
	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics") //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/metrics answered %d on the ingest port", resp.StatusCode)
	}
}

// And the config refuses the arrangement that would put it there.
func TestOneAddressForBothIsRefused(t *testing.T) {
	yaml := fmt.Sprintf(`
name: g
listen: {addr: ":8080"}
metrics: {addr: ":8080"}
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: w, entity: c, source_key: k, record_ts: t}
    sink: {type: files, path: %s/}
    dead_letter: {type: files, path: %s/}
`, t.TempDir(), t.TempDir())
	_, err := load(t, yaml)
	if err == nil {
		t.Fatal("it was accepted")
	}
	if !strings.Contains(err.Error(), "own address") {
		t.Errorf("the refusal does not explain why: %v", err)
	}
}

// Off is a decision somebody made, and absent is not.
func TestMetricsDefaultOnAndTurnOffOnlyWhenSaidSo(t *testing.T) {
	for _, c := range []struct{ name, line, want string }{
		{"a file that says nothing gets them", "", ":9090"},
		{"a file that names an address gets it", `metrics: {addr: ":9999"}`, ":9999"},
		{"a file that says empty means it", `metrics: {addr: ""}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			yaml := fmt.Sprintf(`
name: g
%s
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: w, entity: c, source_key: k, record_ts: t}
    sink: {type: files, path: %s/}
    dead_letter: {type: files, path: %s/}
`, c.line, t.TempDir(), t.TempDir())
			cfg, err := load(t, yaml)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Metrics.Address(); got != c.want {
				t.Errorf("metrics.addr resolved to %q, want %q", got, c.want)
			}
		})
	}
}

func render(t *testing.T, m *gateway.Metrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
