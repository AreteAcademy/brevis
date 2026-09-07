// Package metrics is the engine's metric surface.
//
// It is built on go.opentelemetry.io/otel/sdk/metric with a MANUAL reader,
// which is what makes the pull model honest: nothing is aggregated waiting to be
// shipped, and Collect runs when /metrics is requested. The Prometheus exporter
// is deliberately not here -- it costs 49 packages, 29 of them protobuf, to
// render a text format this package writes in exposition.go. See
// docs/plan/2026-09-08-observability.md section 1, and the forbidden list in
// .github/scripts/engine-weight.sh that holds the decision down.
//
// Every method tolerates a nil receiver. That is not defensiveness: `brevis run`
// executes a workflow with no server and no scrape endpoint, and the alternative
// is either threading a no-op implementation through every constructor or making
// the engine's core depend on a meter existing.
package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
	sdk "go.opentelemetry.io/otel/sdk/metric"
)

// The bucket boundaries are set per instrument and not left to the SDK's
// default, which is [0, 5, 10, 25 ... 10000] -- tuned for milliseconds. Every
// duration here is in SECONDS, so the default would put every observation in
// the first bucket and produce a histogram that is technically populated and
// answers nothing.
var (
	// A claim is the scheduler's own latency: enqueued to picked up. Anything
	// past a minute here means the dispatcher is saturated, so the resolution
	// belongs below the second.
	claimBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300}

	// A step is a pod: seconds to start, and an hour is a normal dbt build.
	stepBuckets = []float64{0.1, 0.5, 1, 5, 15, 30, 60, 300, 600, 1800, 3600, 7200}

	// A run is every step of a workflow, so its ceiling is higher than a
	// step's and its floor is not interesting.
	runBuckets = []float64{1, 5, 15, 30, 60, 300, 600, 1800, 3600, 7200, 21600}
)

// Metrics is the whole surface. It is a struct of instruments rather than an
// interface because there is exactly one implementation and the engine's
// packages should not have to import OpenTelemetry to report a duration.
type Metrics struct {
	reader *sdk.ManualReader

	runs         api.Int64Counter
	runDuration  api.Float64Histogram
	stepDuration api.Float64Histogram
	stepAttempts api.Int64Counter
	claimLatency api.Float64Histogram
	orphans      api.Int64Counter

	alertsDelivered   api.Int64Counter
	alertsUndelivered api.Int64Counter
	alertAttempts     api.Float64Histogram

	// The gauges are observable: their value is read at COLLECT time, from
	// whoever owns it, rather than pushed on every change. Queue depth lives in
	// Postgres and slot usage lives in the dispatcher, and neither wants a
	// write path into this package.
	meter api.Meter
}

// New builds the registry. It does not fail: every error the SDK can return
// here is a programming mistake in this file (a duplicate instrument name, an
// invalid unit), and a process that refuses to boot because a metric name has a
// typo has traded an outage for a warning.
//
// The errors are not swallowed silently either -- an instrument that fails to
// register comes back as a nil interface, and the nil-tolerant methods below
// turn that into "this metric is absent", which the scrape shows.
func New() *Metrics {
	reader := sdk.NewManualReader()
	provider := sdk.NewMeterProvider(sdk.WithReader(reader))
	meter := provider.Meter("github.com/AreteAcademy/brevis")

	m := &Metrics{reader: reader, meter: meter}

	m.runs, _ = meter.Int64Counter("brevis_run_total",
		api.WithDescription("Runs that reached a terminal state."))
	m.runDuration, _ = meter.Float64Histogram("brevis_run_duration_seconds",
		api.WithDescription("Wall time of a run, from queued to terminal."),
		api.WithUnit("s"), api.WithExplicitBucketBoundaries(runBuckets...))
	m.stepDuration, _ = meter.Float64Histogram("brevis_step_duration_seconds",
		api.WithDescription("Wall time of one step of one run."),
		api.WithUnit("s"), api.WithExplicitBucketBoundaries(stepBuckets...))
	m.stepAttempts, _ = meter.Int64Counter("brevis_step_attempts_total",
		api.WithDescription("Attempts started for a step. Flapping shows here and nowhere else."))
	m.claimLatency, _ = meter.Float64Histogram("brevis_claim_latency_seconds",
		api.WithDescription("Queued to claimed. This is the scheduler's own SLA."),
		api.WithUnit("s"), api.WithExplicitBucketBoundaries(claimBuckets...))
	m.orphans, _ = meter.Int64Counter("brevis_orphans_recovered_total",
		api.WithDescription("Items returned to the queue because the worker that claimed them died."))

	m.alertsDelivered, _ = meter.Int64Counter("brevis_alerts_delivered_total",
		api.WithDescription("Alerts that reached their destination."))
	m.alertsUndelivered, _ = meter.Int64Counter("brevis_alerts_undelivered_total",
		api.WithDescription("Alerts given up on. Somebody is waiting for a message that is not coming."))
	m.alertAttempts, _ = meter.Float64Histogram("brevis_alert_attempts",
		api.WithDescription("How many tries an alert took to arrive."),
		api.WithExplicitBucketBoundaries(1, 2, 3, 4, 5, 6, 10))

	return m
}

// RunFinished records one run reaching a terminal state.
//
// `workflow`, `status` and `trigger` are all bounded by what has been published
// and by the state machine. `run_id` is deliberately absent, here and
// everywhere: it is unbounded, and one unbounded label is how a metrics backend
// becomes the most expensive part of an installation.
func (m *Metrics) RunFinished(ctx context.Context, workflow, status, trigger string, d time.Duration) {
	if m == nil {
		return
	}
	attrs := api.WithAttributes(
		attribute.String("workflow", workflow),
		attribute.String("status", status),
		attribute.String("trigger", trigger),
	)
	if m.runs != nil {
		m.runs.Add(ctx, 1, attrs)
	}
	if m.runDuration != nil {
		// The status is on the duration too: a run that failed after four
		// seconds and one that succeeded after four hours have nothing to say
		// to each other, and averaging them is how a latency chart lies.
		m.runDuration.Record(ctx, d.Seconds(), api.WithAttributes(
			attribute.String("workflow", workflow),
			attribute.String("status", status),
		))
	}
}

// StepFinished records one step of one run.
func (m *Metrics) StepFinished(ctx context.Context, workflow, step, status string, d time.Duration) {
	if m == nil || m.stepDuration == nil {
		return
	}
	m.stepDuration.Record(ctx, d.Seconds(), api.WithAttributes(
		attribute.String("workflow", workflow),
		attribute.String("step", step),
		attribute.String("status", status),
	))
}

// StepStarted counts an attempt. It is separate from StepFinished because a
// step that never finishes is exactly the case worth counting.
func (m *Metrics) StepStarted(ctx context.Context, workflow, step string) {
	if m == nil || m.stepAttempts == nil {
		return
	}
	m.stepAttempts.Add(ctx, 1, api.WithAttributes(
		attribute.String("workflow", workflow),
		attribute.String("step", step),
	))
}

// Claimed records how long an item waited in the queue before a worker took it.
//
// No workflow label: this measures the DISPATCHER, and slicing it by workflow
// would invite reading a scheduling delay as a property of the pipeline that
// happened to be waiting.
func (m *Metrics) Claimed(ctx context.Context, waited time.Duration) {
	if m == nil || m.claimLatency == nil {
		return
	}
	m.claimLatency.Record(ctx, waited.Seconds())
}

// OrphansRecovered counts items returned to the queue by the visibility sweep.
// Today this event is a log line and nothing else, which means nobody can alert
// on "workers are dying".
func (m *Metrics) OrphansRecovered(ctx context.Context, n int) {
	if m == nil || m.orphans == nil || n <= 0 {
		return
	}
	m.orphans.Add(ctx, int64(n))
}

// AlertDelivered records an alert that arrived, and how many tries it took.
//
// The attempt count is a metric of its own rather than a label, because it is a
// distribution: "alerts usually arrive first try, and last Tuesday the p99 was
// four" is the sentence an operator wants, and a label would make each count
// its own series.
func (m *Metrics) AlertDelivered(ctx context.Context, channel string, attempts int) {
	if m == nil {
		return
	}
	at := api.WithAttributes(attribute.String("channel", channel))
	if m.alertsDelivered != nil {
		m.alertsDelivered.Add(ctx, 1, at)
	}
	if m.alertAttempts != nil {
		m.alertAttempts.Record(ctx, float64(attempts), at)
	}
}

// AlertUndelivered records an alert that will never arrive.
//
// This is the number worth an alert rule of its own, and the irony is
// deliberate: it is the one case the alerting system cannot tell anybody about
// itself.
func (m *Metrics) AlertUndelivered(ctx context.Context, channel string) {
	if m == nil || m.alertsUndelivered == nil {
		return
	}
	m.alertsUndelivered.Add(ctx, 1, api.WithAttributes(attribute.String("channel", channel)))
}

// WatchAlerts registers the outbox's depth, read at collect time.
//
// `waiting` is alerts still trying. `undelivered` is alerts given up on, and it
// is CUMULATIVE across the table's history rather than a rate -- which is what
// makes it useful as a gauge: a number that stops going up is a problem that
// stopped, and one that is not zero is a message somebody never got.
func (m *Metrics) WatchAlerts(read func(context.Context) (waiting, undelivered int, err error)) error {
	if m == nil || read == nil {
		return nil
	}
	g, err := m.meter.Int64ObservableGauge("brevis_alerts_outbox",
		api.WithDescription("Alerts in the outbox, by whether they can still arrive."))
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(ctx context.Context, o api.Observer) error {
		waiting, undelivered, err := read(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(g, int64(waiting), api.WithAttributes(attribute.String("state", "waiting")))
		o.ObserveInt64(g, int64(undelivered), api.WithAttributes(attribute.String("state", "undelivered")))
		return nil
	}, g)
	return err
}

// WatchQueue registers the queue depth, read at collect time.
//
// It is an observable gauge and not a counter that the queue increments,
// because the queue is shared: several dispatchers write to it and the depth is
// a property of the TABLE, not of this process. Reading it on scrape gives the
// true number; counting local pushes and pops would give this replica's opinion
// of it.
//
// An error from `read` is reported as an absent metric rather than a zero one.
// A queue depth of 0 and a database that cannot be reached must not look the
// same on a chart -- the first is calm and the second is an outage.
func (m *Metrics) WatchQueue(read func(context.Context) (pending, claimed int, err error)) error {
	if m == nil || read == nil {
		return nil
	}
	g, err := m.meter.Int64ObservableGauge("brevis_queue_depth",
		api.WithDescription("Items in queue_items, by whether a worker holds them."))
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(ctx context.Context, o api.Observer) error {
		pending, claimed, err := read(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(g, int64(pending), api.WithAttributes(attribute.String("state", "pending")))
		o.ObserveInt64(g, int64(claimed), api.WithAttributes(attribute.String("state", "claimed")))
		return nil
	}, g)
	return err
}

// WatchSlots registers the concurrency ceiling and what is used of it.
//
// The limit is published as a metric of its own rather than left to the
// operator to remember: "in use is 5" answers nothing without it, and a
// saturation alert wants the ratio, which needs both numbers from the same
// scrape.
func (m *Metrics) WatchSlots(read func() (inUse, limit int)) error {
	if m == nil || read == nil {
		return nil
	}
	inUse, err := m.meter.Int64ObservableGauge("brevis_slots_in_use",
		api.WithDescription("Runs executing in this process right now."))
	if err != nil {
		return err
	}
	limit, err := m.meter.Int64ObservableGauge("brevis_slots_limit",
		api.WithDescription("Maximum simultaneous runs configured for this process."))
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(_ context.Context, o api.Observer) error {
		u, l := read()
		o.ObserveInt64(inUse, int64(u))
		o.ObserveInt64(limit, int64(l))
		return nil
	}, inUse, limit)
	return err
}
