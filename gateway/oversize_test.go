package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
)

// An event too large is archived WHOLE and a reduced one continues, carrying a
// pointer back.
//
// This is the Claim Check pattern, and the reason it beats a flat 413 is that
// an oversized payload is usually the most interesting one somebody has: it is
// the request with the whole document attached, and refusing it throws away the
// case worth debugging.
func TestAnOversizedEventIsArchivedWholeAndReduced(t *testing.T) {
	archive := t.TempDir()
	hooks := gateway.NewHooks()
	hooks.MustRegister("strip", func(e map[string]any) (map[string]any, error) {
		delete(e, "attachment")
		return e, nil
	})

	srv := oversizeGateway(t, hooks, archive, "512", "strip")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := fmt.Sprintf(
		`{"event_id":"big-1","occurred_at":"2026-09-24T10:00:00Z","host":"a.b","attachment":%q}`,
		strings.Repeat("x", 2000))
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The archive has it WHOLE, attachment included.
	archived := readDeadLetter(t, archive)
	if len(archived) != 1 {
		t.Fatalf("the archive holds %d records", len(archived))
	}
	if a, _ := archived[0]["attachment"].(string); len(a) != 2000 {
		t.Errorf("the archive did not get the whole event: attachment is %d bytes", len(a))
	}

	// And the stream got the reduced one, with the claim check on it.
	delivered := readDeadLetter(t, deliveredDir(t, srv))
	if len(delivered) != 1 {
		t.Fatalf("the sink got %d records", len(delivered))
	}
	d := delivered[0]
	if _, still := d["attachment"]; still {
		t.Error("the reduction hook did not run: the attachment is still there")
	}
	if d[gateway.ColumnOversize] != true {
		t.Errorf("the record does not say it was oversized: %v", d)
	}
	if d[gateway.ColumnOversizeArchive] == nil || d[gateway.ColumnOversizeBytes] == nil {
		t.Errorf("the claim check is incomplete, so nothing says where to look: %v", d)
	}
	// The identity survives the reduction, which is what lets the two halves
	// be joined back together.
	if id, _ := d["ingestion_id"].(string); len(id) != 36 {
		t.Errorf("the reduced record lost its identity: %v", d["ingestion_id"])
	}
}

// With no reduction hook the event is archived and dropped, not delivered
// whole and not lost.
//
// Which fields are heavy is domain knowledge and a YAML file cannot hold it, so
// a gateway with no hook has nothing to reduce WITH. Delivering it anyway would
// defeat the limit; refusing it would lose the event. Archived and dropped is
// the third answer, and it is counted.
func TestWithNoReductionHookAnOversizedEventIsArchivedAndDropped(t *testing.T) {
	archive := t.TempDir()
	srv := oversizeGateway(t, nil, archive, "512", "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := fmt.Sprintf(
		`{"event_id":"big-2","occurred_at":"2026-09-24T10:00:00Z","attachment":%q}`,
		strings.Repeat("x", 2000))
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := readDeadLetter(t, archive); len(got) != 1 {
		t.Fatalf("the archive holds %d records, want 1", len(got))
	}
	if got := readDeadLetter(t, deliveredDir(t, srv)); len(got) != 0 {
		t.Errorf("an oversized event reached the sink whole: %v", got)
	}
	// And it is visible: an event that silently stops arriving is the worst
	// outcome here.
	if !strings.Contains(renderMetrics(t, srv), `brevis_gateway_oversized_total{stream="clicks"} 1`) {
		t.Error("nothing counted the archived event")
	}
}

// An event under the limit is untouched: no archive write, no claim check, no
// marshal cost that shows up in the payload.
func TestAnOrdinaryEventIsNotTouchedByTheOversizePath(t *testing.T) {
	archive := t.TempDir()
	srv := oversizeGateway(t, nil, archive, "4KiB", "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("small-1", "a.b"), http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if entries, _ := os.ReadDir(archive); len(entries) != 0 {
		t.Errorf("an ordinary event was archived")
	}
	delivered := readDeadLetter(t, deliveredDir(t, srv))
	if len(delivered) != 1 {
		t.Fatalf("the sink got %d records", len(delivered))
	}
	if _, stamped := delivered[0][gateway.ColumnOversize]; stamped {
		t.Error("an ordinary event was stamped as oversized")
	}
}

// stamp_loaded_at writes the SDK's column, in the SDK's format.
//
// Opt-in, because it adds a field to every record and both a topic's
// subscribers and a table's columns notice.
func TestTheReceiveTimeIsStampedOnlyWhenAsked(t *testing.T) {
	for _, stamp := range []bool{false, true} {
		t.Run(fmt.Sprintf("stamp_loaded_at=%v", stamp), func(t *testing.T) {
			sink := &recordingSink{}
			srv := stampGateway(t, sink, stamp)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			post(t, ts.URL+"/v1/clicks", event("e-1", "a.b"), http.StatusAccepted)
			if err := srv.Close(context.Background()); err != nil {
				t.Fatal(err)
			}

			got := sink.payloads()
			if len(got) != 1 {
				t.Fatalf("the sink saw %d events", len(got))
			}
			at, present := got[0]["ingestion_loaded_at"].(string)
			if present != stamp {
				t.Fatalf("ingestion_loaded_at present=%v, want %v", present, stamp)
			}
			if !stamp {
				return
			}
			// RFC3339, UTC: "2026-09-24T10:00:00Z". The SDK's format, so a row
			// this gateway lands and a row a pipeline lands are one shape.
			if len(at) != 20 || !strings.HasSuffix(at, "Z") {
				t.Errorf("ingestion_loaded_at is %q, which is not the SDK's RFC3339 UTC", at)
			}
		})
	}
}

func oversizeGateway(t *testing.T, hooks *gateway.Hooks, archive, limit, hook string) *gateway.Server {
	t.Helper()
	line := ""
	if hook != "" {
		line = "\n      hook: " + hook
	}
	out := t.TempDir()
	t.Setenv("GW_OUT", out)
	yaml := fmt.Sprintf(`
name: big
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 500, every: 1h}}
    retry: {attempts: 1}
    oversize:
      larger_than: %s
      archive: {type: files, path: %s/}%s
    sink: {type: files, path: %s/}
    dead_letter: {type: files, path: %s/}
`, limit, archive, line, out, t.TempDir())

	file := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(file)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, hooks, everything()...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func stampGateway(t *testing.T, sink gateway.Sinker, stamp bool) *gateway.Server {
	t.Helper()
	yaml := fmt.Sprintf(`
name: stamped
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    stamp_loaded_at: %v
    buffer: {flush: {records: 500, every: 1h}}
    retry: {attempts: 1}
    sink: {type: files, path: %s/}
    dead_letter: {type: files, path: %s/}
`, stamp, t.TempDir(), t.TempDir())

	file := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(file)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, nil, with(gateway.WithSink("clicks", sink))...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// deliveredDir is where the oversize gateway's sink wrote.
func deliveredDir(t *testing.T, _ *gateway.Server) string {
	t.Helper()
	return os.Getenv("GW_OUT")
}

func renderMetrics(t *testing.T, srv *gateway.Server) string {
	t.Helper()
	var b strings.Builder
	if err := srv.Metrics().Render(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

var _ = json.Marshal
