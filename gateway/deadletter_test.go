package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

// A sink that refuses must not lose the events, and before this the refusal was
// a log line and nothing else -- which is losing data quietly, the one failure
// an ingestion service exists not to have.
func TestABatchTheSinkRefusesReachesTheDeadLetterWithTheReason(t *testing.T) {
	dead := t.TempDir()
	srv, ts := serving(t, dead, `{type: files, path: /dev/null/impossible/}`)
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks",
		`{"event_id":"e-1","occurred_at":"2026-09-24T10:00:00Z","host":"a.b"}`,
		http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Logf("close reported: %v", err)
	}

	rows := readDeadLetter(t, dead)
	if len(rows) != 1 {
		t.Fatalf("the dead letter holds %d rows", len(rows))
	}
	r := rows[0]

	// The event survives whole. A dead letter that lost the payload would be a
	// log line with extra steps.
	if r["event_id"] != "e-1" || r["host"] != "a.b" {
		t.Errorf("the event did not survive: %v", r)
	}
	// And the identity, so a replay lands where the original would have.
	if id, _ := r["ingestion_id"].(string); len(id) != 36 {
		t.Errorf("ingestion_id is %q", r["ingestion_id"])
	}

	// The reason travels ON the record, because whoever finds this file has the
	// events and not the log, and "why is this here" is their first question.
	reason, _ := r["_dead_letter_reason"].(string)
	if reason == "" {
		t.Error("no reason on the record")
	}
	if r["_dead_letter_sink"] == nil || r["_dead_letter_at"] == nil {
		t.Errorf("the record does not say which sink or when: %v", r)
	}
}

// It retries before giving up: a sink that blinks must not cost a batch.
func TestTheSinkIsRetriedBeforeTheDeadLetter(t *testing.T) {
	var attempts atomic.Int32
	flaky := &countingSink{
		attempts: &attempts,
		// Fails twice, then works. With attempts: 4 that is a delivery.
		failFirst: 2,
	}

	dead := t.TempDir()
	srv, ts := servingWith(t, dead, flaky, 4, 5*time.Millisecond)
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks",
		`{"event_id":"e-2","occurred_at":"2026-09-24T10:00:00Z","host":"a.b"}`,
		http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Log(err)
	}

	if got := attempts.Load(); got != 3 {
		t.Errorf("the sink was called %d times; two failures then a success is 3", got)
	}
	if rows := readDeadLetter(t, dead); len(rows) != 0 {
		t.Errorf("a batch that eventually landed was also buried: %v", rows)
	}
}

// And it stops: a sink that is down must not be retried for ever, because the
// events are in memory while it is.
func TestItGivesUpAfterTheDeclaredAttempts(t *testing.T) {
	var attempts atomic.Int32
	always := &countingSink{attempts: &attempts, failFirst: 99}

	dead := t.TempDir()
	srv, ts := servingWith(t, dead, always, 3, 5*time.Millisecond)
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks",
		`{"event_id":"e-3","occurred_at":"2026-09-24T10:00:00Z","host":"a.b"}`,
		http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Log(err)
	}

	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts: 3 produced %d calls", got)
	}
	if rows := readDeadLetter(t, dead); len(rows) != 1 {
		t.Errorf("the exhausted batch is not in the dead letter: %v", rows)
	}
}

// A stream with no dead letter is refused at load. Defaulting it to silence
// would put the decision where nobody makes it.
func TestAStreamWithNoDeadLetterIsRefused(t *testing.T) {
	_, err := load(t, strings.Replace(valid, "    dead_letter: {type: files, path: ./dead/}\n", "", 1))
	if err == nil {
		t.Fatal("it was accepted")
	}
	if !strings.Contains(err.Error(), "losing data quietly") {
		t.Errorf("the refusal is: %v", err)
	}
}

// countingSink fails a set number of times, then works.
type countingSink struct {
	attempts  *atomic.Int32
	failFirst int32
}

func (c *countingSink) Describe() string { return "a sink under test" }
func (c *countingSink) Write(_ context.Context, batch []gateway.Envelope) (int64, error) {
	n := c.attempts.Add(1)
	if n <= c.failFirst {
		return 0, fmt.Errorf("refused, attempt %d", n)
	}
	return int64(len(batch)), nil
}

func serving(t *testing.T, dead, sink string) (*gateway.Server, *httptest.Server) {
	t.Helper()
	cfg := gatewayConfig(t, dead, sink, 4, time.Millisecond)
	srv, err := gateway.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, httptest.NewServer(srv.Handler())
}

func readDeadLetter(t *testing.T, dir string) []map[string]any {
	t.Helper()
	var out []map[string]any
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name())) //nolint:gosec // a dir this test made
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var row map[string]any
			if err := json.Unmarshal(sc.Bytes(), &row); err == nil {
				out = append(out, row)
			}
		}
		_ = f.Close()
	}
	return out
}

// gatewayConfig builds one stream with the dead letter and retry a test needs.
func gatewayConfig(t *testing.T, dead, sink string, attempts int, backoff time.Duration) *gateway.Config {
	t.Helper()
	yaml := fmt.Sprintf(`
name: t
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 1, every: 1h}}
    retry: {attempts: %d, backoff: %s, max_backoff: %s}
    sink: %s
    dead_letter: {type: files, path: %s/}
`, attempts, backoff, backoff, sink, dead)

	path := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	return cfg
}

func servingWith(t *testing.T, dead string, sink gateway.Sinker, attempts int, backoff time.Duration) (*gateway.Server, *httptest.Server) {
	t.Helper()
	cfg := gatewayConfig(t, dead, `{type: files, path: /dev/null/unused/}`, attempts, backoff)
	srv, err := gateway.New(cfg, nil, gateway.WithSink("clicks", sink))
	if err != nil {
		t.Fatal(err)
	}
	return srv, httptest.NewServer(srv.Handler())
}
