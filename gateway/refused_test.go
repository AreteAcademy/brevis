package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

// The three refusals the pipe can make, each counted, and each tested for the
// first time.
//
// Before this, `StatusRequestEntityTooLarge` appeared ONCE in the module — at
// the line that sends it. `config_test.go` proved that `max_body: 2MiB`
// parses, and nothing proved the ceiling ever fires. The same for both 400s.
func TestThePipesRefusalsAreCountedAndSaidOutLoud(t *testing.T) {
	for _, c := range []struct {
		name, path, body, reason string
		status                   int
	}{
		{
			name:   "a body over the ceiling",
			path:   "/v1/clicks",
			body:   `{"event_id":"e-1","occurred_at":"2026-01-01T00:00:00Z","pad":"` + strings.Repeat("x", 4096) + `"}`,
			reason: gateway.ReasonBodyTooLarge,
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name:   "a body that is not JSON",
			path:   "/v1/clicks",
			body:   `{"event_id": `,
			reason: gateway.ReasonMalformed,
			status: http.StatusBadRequest,
		},
		{
			// `format: array`, because `format: json` is one body one event:
			// it returns an arrival or an error and can never return none, so
			// the empty refusal is unreachable there. Measured, not assumed.
			name:   "a body carrying no event",
			path:   "/v1/batches",
			body:   `[]`,
			reason: gateway.ReasonEmpty,
			status: http.StatusBadRequest,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, base := refusingGateway(t)
			post(t, base+c.path, c.body, c.status)

			stream := "clicks"
			if c.path == "/v1/batches" {
				stream = "batches"
			}
			want := `brevis_gateway_requests_refused_total{stream="` + stream + `",reason="` + c.reason + `"} 1`
			if got := render(t, srv.Metrics()); !strings.Contains(got, want) {
				t.Errorf("the refusal is not in the scrape:\n  %s\ngot:\n%s", want, got)
			}
		})
	}
}

// A request that is accepted counts nothing on this series. Without it the
// three above would pass with a counter that fires on everything.
func TestAnAcceptedRequestIsNotCountedAsRefused(t *testing.T) {
	srv, base := refusingGateway(t)
	post(t, base+"/v1/clicks", `{"event_id":"e-1","occurred_at":"2026-01-01T00:00:00Z"}`, http.StatusAccepted)

	if got := render(t, srv.Metrics()); strings.Contains(got, "brevis_gateway_requests_refused_total") {
		t.Errorf("an accepted request minted a refusal series:\n%s", got)
	}
}

// A gateway with a small ceiling, so a few kilobytes trip it.
//
// Its own helper rather than asyncGateway's: that one writes no `listen:`
// block at all, and `max_body` is the whole point of one of the cases.
func refusingGateway(t *testing.T) (*gateway.Server, string) {
	t.Helper()
	dir := t.TempDir()
	yaml := `
name: refusing
listen:
  max_body: 1KiB
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 1000, every: 1h}}
    sink: {type: files, path: ` + dir + `/out/}
    dead_letter: {type: files, path: ` + dir + `/dead/}
  - name: batches
    path: /v1/batches
    format: array
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 1000, every: 1h}}
    sink: {type: files, path: ` + dir + `/out2/}
    dead_letter: {type: files, path: ` + dir + `/dead2/}
`
	path := dir + "/g.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, nil, everything()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts.URL
}
