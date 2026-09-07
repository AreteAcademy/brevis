package sdk

// Meter receives a pipeline's numbers.
//
// It is an INTERFACE in this package and nothing else, which is the same shape
// `to.Files` uses for a Store: declaring it here costs zero dependencies, so a
// fetcher that reads a CSV does not link a telemetry stack to publish a
// counter. The implementations live in subpackages -- see sdk/metrics/otel --
// and only whoever wants one imports it.
//
// The alternative was importing OpenTelemetry here. That adds around two
// hundred packages to EVERY fetcher, including the ones that set no Meter at
// all, and .github/scripts/pruning-check.sh fails when it happens.
//
// Two calls, on purpose. A Counter answers "how many" and a Histogram answers
// "how long" or "how big", and everything a data pipeline reports is one of the
// two. Gauges are absent because a process that lives for ninety seconds has no
// current value worth sampling -- by the time anything reads it, the process is
// gone.
type Meter interface {
	// Counter adds to a running total. `value` may be any non-negative number;
	// it is not restricted to 1, because a load that wrote 48,213 rows should
	// be one call and not 48,213.
	Counter(name string, value int64, attrs ...Attr)

	// Histogram records one observation of a distribution.
	Histogram(name string, value float64, attrs ...Attr)
}

// Attr is one label on a metric.
//
// A string value and not `any`: every backend this could reach turns a label
// into a string in the end, and accepting `any` only moves the decision about
// how a float becomes a label into the implementation, where the consumer
// cannot see it.
//
// KEEP THE VALUES BOUNDED. A label whose value is a record id, a URL with a
// cursor, or a timestamp is how a metrics backend becomes the most expensive
// part of an installation. The SDK's own labels are the pipeline's name and the
// outcome, and nothing else.
type Attr struct{ Key, Value string }

// A returns an Attr, for calls that would otherwise be mostly punctuation.
//
//	m.Counter("vendor_rejected_total", 12, sdk.A("reason", "missing_cnpj"))
func A(key, value string) Attr { return Attr{Key: key, Value: value} }

// The metrics the SDK reports on its own, for whoever is writing a dashboard.
//
// They are prefixed brevis_sdk_ to sit beside the engine's brevis_ series
// without colliding, and the units are in the names because a number called
// `extract_time` tells nobody whether it is seconds or milliseconds.
const (
	MetricRuns         = "brevis_sdk_runs_total"           // counter, attrs: pipeline, status
	MetricRecords      = "brevis_sdk_records_total"        // counter, attrs: pipeline
	MetricRows         = "brevis_sdk_rows_total"           // counter, attrs: pipeline
	MetricRowsIgnored  = "brevis_sdk_rows_ignored_total"   // counter, attrs: pipeline
	MetricPages        = "brevis_sdk_pages_total"          // counter, attrs: pipeline
	MetricAttempts     = "brevis_sdk_http_attempts_total"  // counter, attrs: pipeline
	MetricExtractBytes = "brevis_sdk_extract_bytes_total"  // counter, attrs: pipeline
	MetricLoadBytes    = "brevis_sdk_load_bytes_total"     // counter, attrs: pipeline
	MetricExtract      = "brevis_sdk_extract_seconds"      // histogram, attrs: pipeline
	MetricLoad         = "brevis_sdk_load_seconds"         // histogram, attrs: pipeline
	MetricRowsRejected = "brevis_sdk_rows_rejected_total"  // counter, attrs: pipeline
	MetricSourceFailed = "brevis_sdk_sources_failed_total" // counter, attrs: pipeline
)

// report routes what the pipeline ALREADY counted to the Meter.
//
// This is the design point worth stating: no consumer writes a counter to get
// the standard metrics. Every number below is one the SDK was already tracking
// and already printing in the result line -- Meter only decides where else it
// goes. What the consumer's own calls are for is the numbers only their
// pipeline knows: "rows the vendor rejected", "quota remaining", "orders past
// their SLA".
//
// A nil Meter is the normal case and does nothing.
func report(m Meter, pipeline string, res *Result, err error) {
	if m == nil {
		return
	}
	on := Attr{Key: "pipeline", Value: pipeline}

	status := "success"
	if err != nil {
		status = "failed"
	}
	// Counted even when res is nil: a run that failed before producing a
	// result is exactly the one a failure rate has to include, and skipping it
	// would make the rate look better the worse things got.
	m.Counter(MetricRuns, 1, on, Attr{Key: "status", Value: status})
	if res == nil {
		return
	}

	// Zeros are skipped rather than sent. A counter that receives 0 is
	// indistinguishable from one that was never called, so sending it buys
	// nothing -- and NOT sending it keeps a fetcher that never paginates from
	// creating a pages series that is permanently flat.
	count := func(name string, v int64) {
		if v != 0 {
			m.Counter(name, v, on)
		}
	}
	count(MetricRecords, res.Records)
	count(MetricRows, res.Rows)
	count(MetricRowsIgnored, res.Ignored)
	count(MetricPages, int64(res.Pages))
	count(MetricAttempts, int64(res.Attempts))
	count(MetricExtractBytes, res.ExtractBytes)
	count(MetricLoadBytes, res.Bytes)
	count(MetricRowsRejected, int64(len(res.RowErrors)))
	count(MetricSourceFailed, int64(len(res.FailedSources)))

	// The durations go out even at zero: a histogram's count is how many runs
	// happened, and dropping the fast ones would bias every percentile upward.
	m.Histogram(MetricExtract, res.ExtractTime.Seconds(), on)
	m.Histogram(MetricLoad, res.LoadTime.Seconds(), on)
}
