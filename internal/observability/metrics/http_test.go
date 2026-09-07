package metrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestTheEndpointAnswersEvenWithNothingRecorded is the default path, which is
// the one everybody hits first. A fresh process has recorded nothing, and the
// scrape has to be a valid empty one -- not a 404, not a panic. "Not configured
// yet" and "broken" must not look the same to whoever is wiring up a collector.
func TestTheEndpointAnswersEvenWithNothingRecorded(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, wanted 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != ContentType {
		t.Errorf("Content-Type = %q, wanted %q", got, ContentType)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("a process that did nothing reported:\n%s", body)
	}
}

// TestANilRegistryStillAnswers: `brevis run` has no registry, and if the
// endpoint were ever mounted there it must not panic on the first scrape.
func TestANilRegistryStillAnswers(t *testing.T) {
	var m *Metrics
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, wanted 200", rec.Code)
	}
}

// TestAFailedCollectIsA500AndNotAnEmpty200.
//
// The queue depth is read from Postgres at collect time. If the database is
// unreachable, answering 200 with the other metrics would tell the scraper the
// target is healthy and quietly drop the series -- and a queue-depth chart that
// goes flat looks exactly like a queue that went quiet. A 500 marks the target
// down, which is the truth.
func TestAFailedCollectIsA500AndNotAnEmpty200(t *testing.T) {
	m := New()
	m.RunFinished(context.Background(), "w", "success", "cron", time.Second)
	down := errors.New("dial tcp: connection refused")
	if err := m.WatchQueue(func(context.Context) (int, int, error) { return 0, 0, down }); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, wanted 500 -- a scrape of a broken dependency must not read as healthy", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the body does not say what broke: %q", rec.Body.String())
	}
	// And nothing partial: a scraper reading a truncated exposition does not
	// error, it records what it managed to parse.
	if strings.Contains(rec.Body.String(), "brevis_run_total") {
		t.Errorf("a half-written scrape reached the wire:\n%s", rec.Body.String())
	}
}

// TestABusyPortDoesNotTakeTheProcessDown. Two Brevis processes on one
// developer's machine is the common case, and killing the second one to protect
// a scrape endpoint would be the wrong trade every time.
func TestABusyPortDoesNotTakeTheProcessDown(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Serve returns immediately and reports the bind failure on its own
	// goroutine; the assertion is that the test process is still here.
	New().Serve(ctx, occupied.Addr().String(), quiet())
	time.Sleep(50 * time.Millisecond)
}

// TestTheListenerActuallyServes closes the loop that every unit test above
// leaves open: the handler is correct, and the thing that mounts it on a socket
// works too.
func TestTheListenerActuallyServes(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close()

	m := New()
	m.RunFinished(context.Background(), "daily_sales", "success", "cron", time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Serve(ctx, addr, quiet())

	var body string
	for range 50 {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body = string(b)
		break
	}
	if !strings.Contains(body, `brevis_run_total{status="success",trigger="cron",workflow="daily_sales"} 1`) {
		t.Errorf("the listener did not serve the metrics:\n%s", body)
	}
}

// TestNothingIsServedWhenTheAddressIsEmpty. BREVIS_METRICS_ADDR="" is how an
// installation turns this off, and it has to mean that -- config.optional
// exists so empty and unset are different, and this is the half of that
// decision that lives here.
func TestNothingIsServedWhenTheAddressIsEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	New().Serve(ctx, "", quiet()) // must not panic and must not listen
}
