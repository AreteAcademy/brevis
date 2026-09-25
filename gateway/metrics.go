package gateway

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// The series the gateway reports, prefixed so they sit beside the engine's
// brevis_ and the SDK's brevis_sdk_ without colliding.
//
// The units are in the names, because a number called `delivery_time` tells
// nobody whether it is seconds or milliseconds.
const (
	MetricReceived  = "brevis_gateway_events_received_total"
	MetricRejected  = "brevis_gateway_events_rejected_total"
	MetricDropped   = "brevis_gateway_events_dropped_total"
	MetricBatches   = "brevis_gateway_batches_total"
	MetricBuried    = "brevis_gateway_dead_letter_records_total"
	MetricSaturated = "brevis_gateway_saturated_total"
	MetricOversized = "brevis_gateway_oversized_total"
	MetricDelivery  = "brevis_gateway_delivery_seconds"
	MetricBatchSize = "brevis_gateway_batch_records"
	MetricBuffer    = "brevis_gateway_buffer_records"
	MetricQueue     = "brevis_gateway_queue_batches"
)

// Why a reason is a CLOSED enum and never the error text.
//
// The rule is the SDK's, written where it costs the most: "a label whose value
// is a record id, a URL with a cursor, or a timestamp is how a metrics backend
// becomes the most expensive part of an installation." An error string is
// worse than all three -- it carries a table name, a column, sometimes a row --
// and one malformed client would mint a new series per request.
//
// The text belongs in the dead letter, on the record, where whoever has the
// events can read it. These say only which KIND of thing happened.
const (
	ReasonHook      = "hook_error"
	ReasonSourceKey = "no_source_key"
	ReasonRecordTS  = "no_record_ts"
	ReasonIdentity  = "identity"
	ReasonOversize  = "oversize"
	ReasonAdmit     = "sink_refused"
)

// Outcomes of one batch's delivery.
const (
	OutcomeDelivered = "delivered"
	OutcomeRetried   = "retried"
	OutcomeBuried    = "buried"
)

// Metrics is what the gateway counts.
//
// Hand-written, with no dependency at all, and that is a measurement rather
// than a preference: the OpenTelemetry SDK costs 40 packages here and
// prometheus/client_golang 43, to report ten instruments whose exposition
// format is `name{label="value"} 42` and has not changed in a decade. The slim
// build exists to not pay for what it does not use, and metrics are in BOTH
// builds.
//
// The engine made the same call one level up for the same reason, and its
// comment names the two things this gets wrong if written carelessly. Both have
// a test here:
//
//   - a histogram's buckets are RUNNING TOTALS in this format, not per-bucket
//     counts. Backwards, it produces a chart that looks plausible and is wrong.
//   - a label value is quoted, so a quote, a backslash or a newline inside one
//     ends the series early and corrupts every line after it.
type Metrics struct {
	received  *counters
	rejected  *counters
	dropped   *counters
	batches   *counters
	buried    *counters
	saturated *counters
	oversized *counters

	delivery  *histograms
	batchSize *histograms

	// depth reports what is held right now. A gauge and not a counter, and the
	// difference from the SDK's Meter -- which has no gauges on purpose -- is
	// honest: a fetcher lives ninety seconds and has no current value worth
	// sampling, a gateway lives for weeks and its buffer depth is exactly that.
	//
	// Read at SCRAPE time from the pipes, so nothing is recorded per event.
	depth func() []gauge
}

type gauge struct {
	name   string
	labels []string
	value  int64
}

// NewMetrics builds the instruments. A nil *Metrics records nothing and serves
// an empty scrape, which is what "metrics are off" has to look like: a valid
// scrape of a process with no metrics, not a 404 that reads as broken.
func NewMetrics() *Metrics {
	return &Metrics{
		received:  newCounters(MetricReceived, "events accepted into a stream's buffer", "stream", "format"),
		rejected:  newCounters(MetricRejected, "events refused before they were buffered", "stream", "reason"),
		dropped:   newCounters(MetricDropped, "events a hook dropped on purpose", "stream"),
		batches:   newCounters(MetricBatches, "batches by what became of them", "stream", "sink", "outcome"),
		buried:    newCounters(MetricBuried, "records written to a dead letter", "stream", "sink"),
		saturated: newCounters(MetricSaturated, "requests refused because the buffer was full", "stream"),
		oversized: newCounters(MetricOversized, "events archived whole because they were too large", "stream"),

		// Prometheus' own default spread, which covers a Pub/Sub publish
		// (milliseconds) and a COPY that is having a bad day (seconds).
		delivery: newHistograms(MetricDelivery, "how long one batch took to deliver, retries included",
			[]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}, "stream", "sink"),
		// A flush of 500 is the default, so the spread straddles it: a stream
		// flushing at 1 and a stream flushing at 5,000 are different problems.
		batchSize: newHistograms(MetricBatchSize, "how many records were in a batch",
			[]float64{1, 5, 10, 50, 100, 500, 1000, 5000}, "stream"),
	}
}

func (m *Metrics) count(c *counters, n int64, labels ...string) {
	if m == nil || n == 0 {
		return
	}
	c.add(n, labels...)
}

func (m *Metrics) observe(h *histograms, v float64, labels ...string) {
	if m == nil {
		return
	}
	h.observe(v, labels...)
}

// counters is one counter series with a fixed set of label names.
type counters struct {
	name, help string
	labelNames []string

	mu sync.RWMutex
	by map[string]*atomic.Int64
}

func newCounters(name, help string, labelNames ...string) *counters {
	return &counters{name: name, help: help, labelNames: labelNames, by: map[string]*atomic.Int64{}}
}

func (c *counters) add(n int64, labels ...string) {
	key := strings.Join(labels, "\x00")

	c.mu.RLock()
	v, ok := c.by[key]
	c.mu.RUnlock()
	if ok {
		v.Add(n)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok = c.by[key]; ok {
		v.Add(n)
		return
	}
	v = &atomic.Int64{}
	v.Add(n)
	c.by[key] = v
}

// histograms is one histogram series: cumulative buckets, a sum and a count.
type histograms struct {
	name, help string
	labelNames []string
	bounds     []float64

	mu sync.Mutex
	by map[string]*bucketSet
}

type bucketSet struct {
	counts []uint64 // one per bound, plus +Inf at the end
	sum    float64
	total  uint64
}

func newHistograms(name, help string, bounds []float64, labelNames ...string) *histograms {
	return &histograms{
		name: name, help: help, labelNames: labelNames, bounds: bounds,
		by: map[string]*bucketSet{},
	}
}

func (h *histograms) observe(v float64, labels ...string) {
	key := strings.Join(labels, "\x00")

	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.by[key]
	if !ok {
		b = &bucketSet{counts: make([]uint64, len(h.bounds)+1)}
		h.by[key] = b
	}
	b.sum += v
	b.total++
	// The bucket a value falls in, and ONLY that one. The running totals are
	// computed when the scrape is written -- storing them cumulatively here
	// would make every observation O(buckets).
	i := sort.SearchFloat64s(h.bounds, v)
	if i < len(h.bounds) && h.bounds[i] < v {
		i++
	}
	b.counts[i]++
}
