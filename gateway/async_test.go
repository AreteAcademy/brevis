package gateway_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

// The caller does not pay the sink's latency.
//
// This is the whole point of the change, and it is the one thing that cannot be
// asserted by reading the code: before it, the request that happened to fill a
// batch ran the delivery inline, so one caller in every `flush.records` paid the
// full round trip -- and, when the sink was down, the full retry window. A p99
// shaped by which caller was unlucky is not a p99 anybody can act on.
func TestTheCallerDoesNotWaitForTheSink(t *testing.T) {
	const delay = 300 * time.Millisecond
	sink := &slowSink{delay: delay}

	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// flush.records is 1, so EVERY request fills a batch. Under the old shape
	// every one of these would have paid `delay`.
	const requests = 5
	start := time.Now()
	for i := range requests {
		post(t, ts.URL+"/v1/clicks", event(fmt.Sprintf("e-%d", i), "a.b"), http.StatusAccepted)
	}
	elapsed := time.Since(start)

	// One delay is the budget for all five, and generously: the point is the
	// difference between "the requests are independent of the sink" and "each
	// one waits", which is 5*300ms against roughly nothing.
	if elapsed > delay {
		t.Fatalf("%d requests took %v against a sink that takes %v each: "+
			"the delivery is on the request path", requests, elapsed, delay)
	}

	// And they really were delivered, not dropped to make the clock look good.
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if got := sink.count.Load(); got != requests {
		t.Errorf("the sink saw %d events, want %d", got, requests)
	}
}

// A sink that is down does not reach the caller either -- until the buffer is
// full, at which point the gateway says so.
//
// The two halves are one property: the queue absorbs, and when it cannot, the
// answer is a 503 rather than a 202 for an event with nowhere to go. An
// accepted event that is never delivered is the one outcome this service exists
// to not have.
func TestAStuckSinkAnswers503RatherThanAcceptingForever(t *testing.T) {
	sink := newBlockingSink()
	defer sink.release()

	// One event per batch, one worker, one queue slot and a ceiling of 4
	// events: the smallest arrangement in which saturation is reachable. The
	// first batch goes to the worker, the second to the queue, and the next
	// four fill the buffer -- so six requests are accepted and the rest are
	// told to come back.
	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 4, `workers: 1`, `queue: 1`)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The first request's batch goes straight to the one worker, which blocks
	// there. Everything after it accumulates, because a queue of zero has
	// nowhere to put a batch.
	var refused, accepted int
	for i := range 20 {
		code := postCode(t, ts.URL+"/v1/clicks", event(fmt.Sprintf("e-%d", i), "a.b"))
		switch code {
		case http.StatusAccepted:
			accepted++
		case http.StatusServiceUnavailable:
			refused++
		default:
			t.Fatalf("request %d answered %d", i, code)
		}
	}
	if refused == 0 {
		t.Fatalf("a stuck sink accepted all %d requests: the buffer has no ceiling", accepted)
	}

	// The refusal is the one a client can act on: wait this long, send the same
	// thing again.
	resp, err := http.Post(ts.URL+"/v1/clicks", //nolint:gosec,noctx
		"application/json", strings.NewReader(event("e-again", "a.b")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected the stream to still be saturated, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 503 with no Retry-After leaves the client guessing")
	}

	// Nothing that was accepted is lost. The sink comes back, the drain
	// delivers what the buffer held, and the count matches what was promised.
	sink.release()
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if got := int(sink.count.Load()); got != accepted {
		t.Errorf("the gateway accepted %d events and delivered %d", accepted, got)
	}
}

// A body re-sent after a 503 is the same record, which is what makes the 503
// safe to act on.
//
// Most services cannot say this: retrying a POST there means risking a
// duplicate. Here the ingestion_id is a frozen UUID v5 over the event's own
// provider|entity|source_key|record_ts, so the retry carries the id the first
// attempt would have -- and a `merge` sink absorbs it rather than writing it
// twice.
func TestARetryAfterA503CarriesTheSameIngestionID(t *testing.T) {
	sink := &recordingSink{}
	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := event("e-1", "a.b")
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	ids := sink.ids()
	if len(ids) != 2 {
		t.Fatalf("the sink saw %d events", len(ids))
	}
	if ids[0] != ids[1] {
		t.Errorf("the same body produced two ids: %s and %s", ids[0], ids[1])
	}
}

// Close waits for what is in flight. A deploy that returned before its
// deliveries finished would lose them, and `memory` losing on a clean stop as
// well as on a crash would make the tier useless rather than merely limited.
func TestCloseWaitsForWhatIsInFlight(t *testing.T) {
	sink := &slowSink{delay: 200 * time.Millisecond}
	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for i := range 3 {
		post(t, ts.URL+"/v1/clicks", event(fmt.Sprintf("e-%d", i), "a.b"), http.StatusAccepted)
	}
	// The deliveries are still running here, by construction.
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if got := sink.count.Load(); got != 3 {
		t.Errorf("Close returned with %d of 3 delivered", got)
	}
}

// A drain that outlasts its window is reported, not waited on.
//
// One batch can hold the full retry window, and a shutdown that blocks past its
// deadline is a pod the orchestrator kills -- which loses the events anyway and
// says nothing about why.
func TestCloseReportsADrainThatOutlastsItsWindow(t *testing.T) {
	sink := newBlockingSink()
	defer sink.release()

	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("e-1", "a.b"), http.StatusAccepted)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := srv.Close(ctx)
	if err == nil {
		t.Fatal("Close said the drain finished")
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("the error does not say what happened: %v", err)
	}
	// WITH the count. "context deadline exceeded" does not tell an operator
	// whether they lost four events or forty thousand, and that difference is
	// the one between a note and an incident.
	if !strings.Contains(err.Error(), "1 accepted event(s)") {
		t.Errorf("the error does not say how many were lost: %v", err)
	}
	if got := srv.Pending(); got != 1 {
		t.Errorf("Pending() = %d, want the one event still undelivered", got)
	}
}

// A clean drain leaves nothing pending, which is what makes the count above
// mean something: a number that is never zero measures nothing.
func TestACleanDrainLeavesNothingPending(t *testing.T) {
	sink := &slowSink{delay: 20 * time.Millisecond}
	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for i := range 3 {
		post(t, ts.URL+"/v1/clicks", event(fmt.Sprintf("e-%d", i), "a.b"), http.StatusAccepted)
	}
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	if got := srv.Pending(); got != 0 {
		t.Errorf("Pending() = %d after a clean drain, want 0", got)
	}
}

// Every stream is reported, not the first one to fail.
//
// One stream running out of budget says nothing about the others, and an
// operator reading "stream A lost 12" while stream B silently lost 4,000 is
// worse served than by both lines.
func TestCloseReportsEveryStreamThatFailed(t *testing.T) {
	sink := newBlockingSink()
	defer sink.release()

	srv := asyncGateway(t, sink, `{records: 1, every: 1h}`, 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post(t, ts.URL+"/v1/clicks", event("e-1", "a.b"), http.StatusAccepted)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := srv.Close(ctx)
	if err == nil {
		t.Fatal("Close said the drain finished")
	}
	// One stream in this fixture, so the join holds one error -- what is
	// pinned here is that Close builds a join at all rather than returning the
	// first and discarding the rest.
	if errs, ok := err.(interface{ Unwrap() []error }); !ok {
		t.Errorf("Close did not return a joined error: %T", err)
	} else if len(errs.Unwrap()) != 1 {
		t.Errorf("the join holds %d errors, want 1", len(errs.Unwrap()))
	}
}

// postCode posts and returns the status, where `post` asserts one. Saturation
// is the one place a test needs to count how many of each came back.
func postCode(t *testing.T, url, payload string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(payload)) //nolint:gosec,noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// slowSink takes its time, which is what a real one does.
type slowSink struct {
	delay time.Duration
	count atomic.Int64
}

func (s *slowSink) Describe() string { return "a slow sink" }
func (s *slowSink) Write(_ context.Context, batch []gateway.Envelope) (int64, error) {
	time.Sleep(s.delay)
	s.count.Add(int64(len(batch)))
	return int64(len(batch)), nil
}

// blockingSink is a sink that is down: it holds every call until released.
type blockingSink struct {
	gate  chan struct{}
	once  sync.Once
	count atomic.Int64
}

func newBlockingSink() *blockingSink { return &blockingSink{gate: make(chan struct{})} }

func (b *blockingSink) Describe() string { return "a sink that is down" }
func (b *blockingSink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	select {
	case <-b.gate:
		b.count.Add(int64(len(batch)))
		return int64(len(batch)), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (b *blockingSink) release() { b.once.Do(func() { close(b.gate) }) }

// recordingSink keeps the ingestion_id of everything it was given.
type recordingSink struct {
	mu   sync.Mutex
	seen []string
	rows []map[string]any
}

func (r *recordingSink) Describe() string { return "a recording sink" }
func (r *recordingSink) Write(_ context.Context, batch []gateway.Envelope) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range batch {
		row, _ := e.Payload.(map[string]any)
		id, _ := row["ingestion_id"].(string)
		r.seen = append(r.seen, id)
		r.rows = append(r.rows, row)
	}
	return int64(len(batch)), nil
}

// payloads is what the sink was handed, whole.
func (r *recordingSink) payloads() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.rows...)
}

func (r *recordingSink) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func asyncGateway(t *testing.T, sink gateway.Sinker, flush string, maxRecords int, extra ...string) *gateway.Server {
	t.Helper()
	buffer := "flush: " + flush
	for _, e := range extra {
		buffer += "\n      " + e
	}
	if maxRecords > 0 {
		buffer += fmt.Sprintf("\n      max_records: %d", maxRecords)
	}
	yaml := fmt.Sprintf(`
name: async
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    retry: {attempts: 1}
    buffer:
      %s
    sink: {type: files, path: %s/}
    dead_letter: {type: files, path: %s/}
`, buffer, t.TempDir(), t.TempDir())

	path := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, nil, with(gateway.WithSink("clicks", sink))...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}
