package otelmeter

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// collectorStub stands in for an OpenTelemetry Collector: it accepts the OTLP
// HTTP POST and keeps what arrived.
type collectorStub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*collector.ExportMetricsServiceRequest
}

func newCollector(t *testing.T) *collectorStub {
	t.Helper()
	c := &collectorStub{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer func() { _ = gz.Close() }()
			body = gz
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req collector.ExportMetricsServiceRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.requests = append(c.requests, &req)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write([]byte{})
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collectorStub) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// metric finds a metric by name across everything that arrived.
func (c *collectorStub) metric(name string) *metricspb.Metric {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, req := range c.requests {
		for _, rm := range req.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() == name {
						return m
					}
				}
			}
		}
	}
	return nil
}

// endpoint strips the scheme: otlpmetrichttp wants host:port.
func (c *collectorStub) endpoint() string {
	return strings.TrimPrefix(c.URL, "http://")
}

func newMeter(t *testing.T, c *collectorStub) *Meter {
	t.Helper()
	m, err := New(context.Background(), Options{
		Endpoint: c.endpoint(),
		Insecure: true,
		// Far longer than the test, so nothing is delivered by the ticker.
		// Whatever arrives, arrived because of Close.
		Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestCloseIsWhatDelivers is the reason this package exists in the shape it
// does. A fetcher is a pod that lives ninety seconds; the periodic reader's
// interval is tens of seconds, and a run that takes twelve exits before the
// first tick. Without a flush on the way out, every number it recorded dies
// with the process -- silently, which is the worst way for telemetry to fail.
func TestCloseIsWhatDelivers(t *testing.T) {
	c := newCollector(t)
	m := newMeter(t, c)

	m.Counter("brevis_sdk_rows_total", 48213)

	if n := c.count(); n != 0 {
		t.Fatalf("%d export(s) arrived before Close; the test cannot tell what delivered them", n)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := c.count(); n == 0 {
		t.Fatal("nothing reached the collector: a fetcher using this would report nothing, in silence")
	}

	got := c.metric("brevis_sdk_rows_total")
	if got == nil {
		t.Fatal("the export arrived without the counter in it")
	}
	if v := got.GetSum().GetDataPoints()[0].GetAsInt(); v != 48213 {
		t.Errorf("the counter arrived as %d", v)
	}
}

// TestNothingArrivesWithoutClose is the negative that gives the test above its
// meaning. If metrics were delivered anyway, the flush would be decoration and
// the previous test would pass whether or not Close did anything.
func TestNothingArrivesWithoutClose(t *testing.T) {
	c := newCollector(t)
	m := newMeter(t, c)
	t.Cleanup(func() { _ = m.Close(context.Background()) })

	m.Counter("brevis_sdk_rows_total", 1)
	time.Sleep(100 * time.Millisecond)

	if n := c.count(); n != 0 {
		t.Errorf("%d export(s) arrived on their own, so Close is not what this package turns on", n)
	}
}

// TestASecondsHistogramGetsSecondsBuckets.
//
// The SDK's default boundaries are [0, 5, 10, 25 ... 10000] and tuned for
// MILLISECONDS. An extract that takes four minutes lands in the first bucket
// under them, and the histogram is technically populated and answers nothing --
// a chart that looks plausible and is wrong.
func TestASecondsHistogramGetsSecondsBuckets(t *testing.T) {
	c := newCollector(t)
	m := newMeter(t, c)

	m.Histogram("brevis_sdk_extract_seconds", 240)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := c.metric("brevis_sdk_extract_seconds")
	if got == nil {
		t.Fatal("the histogram never arrived")
	}
	point := got.GetHistogram().GetDataPoints()[0]
	bounds := point.GetExplicitBounds()
	if len(bounds) != len(secondsBuckets) || bounds[len(bounds)-1] != secondsBuckets[len(secondsBuckets)-1] {
		t.Fatalf("boundaries are %v, wanted %v", bounds, secondsBuckets)
	}

	// And the observation has to land somewhere useful. 240 seconds belongs
	// between 60 and 300, not in the overflow and not in the first bucket.
	counts := point.GetBucketCounts()
	for i, n := range counts {
		if n == 0 {
			continue
		}
		if i == 0 {
			t.Errorf("240 seconds landed in the first bucket -- these are millisecond boundaries")
		}
		if i == len(counts)-1 {
			t.Errorf("240 seconds landed in the overflow bucket")
		}
	}
}

// TestAHistogramNotNamedSecondsKeepsTheDefault: the convention is the contract,
// and it only works if it is actually selective. A consumer counting rows per
// batch must not have seconds boundaries imposed on them.
func TestAHistogramNotNamedSecondsKeepsTheDefault(t *testing.T) {
	c := newCollector(t)
	m := newMeter(t, c)

	m.Histogram("vendor_batch_size", 40)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := c.metric("vendor_batch_size")
	if got == nil {
		t.Fatal("the histogram never arrived")
	}
	bounds := got.GetHistogram().GetDataPoints()[0].GetExplicitBounds()
	if len(bounds) == len(secondsBuckets) && bounds[0] == secondsBuckets[0] {
		t.Errorf("the *_seconds view captured a metric that is not a duration: %v", bounds)
	}
}

// TestTheSameInstrumentIsResolvedOnce. A counter created per call asks the SDK
// to resolve the same name on every row, and OpenTelemetry warns about a
// duplicate registration -- a warning nobody inside a task pod will ever read.
func TestTheSameInstrumentIsResolvedOnce(t *testing.T) {
	c := newCollector(t)
	m := newMeter(t, c)

	for range 100 {
		m.Counter("brevis_sdk_rows_total", 1)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := c.metric("brevis_sdk_rows_total")
	if got == nil {
		t.Fatal("the counter never arrived")
	}
	points := got.GetSum().GetDataPoints()
	if len(points) != 1 {
		t.Fatalf("%d data points for one counter with one label set", len(points))
	}
	if v := points[0].GetAsInt(); v != 100 {
		t.Errorf("100 calls of 1 summed to %d", v)
	}
}

// TestACollectorThatIsDownDoesNotHangTheProcess. A pod whose exit waits on an
// unreachable collector is a Job that Kubernetes eventually kills, and the run
// is recorded as a failure for a telemetry problem.
func TestACollectorThatIsDownDoesNotHangTheProcess(t *testing.T) {
	m, err := New(context.Background(), Options{
		// A port nothing listens on.
		Endpoint: "127.0.0.1:1", Insecure: true, Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Counter("brevis_sdk_rows_total", 1)

	done := make(chan struct{})
	go func() { _ = m.Close(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Close never returned with the collector down")
	}
}
