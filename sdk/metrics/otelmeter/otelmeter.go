// Package otelmeter implements sdk.Meter over OpenTelemetry, pushing over OTLP.
//
// It is a package of its own, and importing it is what makes a fetcher pay for
// it. A fetcher that reads a CSV and sets no Meter links none of this, and
// .github/scripts/pruning-check.sh fails if that ever stops being true.
//
// PUSH, and not the pull the engine uses -- and the difference is not an
// inconsistency, it is the shape of the process. The engine's API and scheduler
// are long-lived, so a collector scrapes them. A fetcher is a pod that lives
// ninety seconds: by the time anything discovers it and scrapes it, it is gone.
// A short-lived process has to push before it exits, which is why Close matters
// more here than anything else in this file.
//
// The directory is metrics/otelmeter and not metrics/otel on purpose: a package
// literally named `otel` collides with go.opentelemetry.io/otel at every call
// site that uses both.
package otelmeter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	api "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/AreteAcademy/brevis/sdk"
)

// secondsBuckets are the boundaries for anything named *_seconds.
//
// The SDK's default is [0, 5, 10, 25 ... 10000], tuned for milliseconds. An
// extract that takes four minutes lands in the first bucket under that default,
// and the histogram is technically populated and answers nothing.
//
// The convention is the contract: name a duration `*_seconds` and it gets
// these. It is the same convention Prometheus asks for, and the SDK's own two
// duration metrics follow it.
var secondsBuckets = []float64{0.1, 0.5, 1, 5, 15, 30, 60, 300, 600, 1800, 3600, 7200}

// Meter is an sdk.Meter backed by an OTLP exporter.
type Meter struct {
	provider *sdkmetric.MeterProvider
	meter    api.Meter

	// Instruments are cached by name. Creating one per call would ask the SDK
	// to resolve the same name on every row, and OpenTelemetry warns about a
	// duplicate registration whose description differs -- a warning nobody in a
	// task pod is going to read.
	mu         sync.Mutex
	counters   map[string]api.Int64Counter
	histograms map[string]api.Float64Histogram
}

// Options configure the exporter. The zero value is the useful one.
type Options struct {
	// Endpoint is the collector. Empty reads OTEL_EXPORTER_OTLP_ENDPOINT from
	// the environment, which is the variable every OpenTelemetry tool already
	// honours -- inventing a BREVIS_-prefixed one would mean an installation
	// configuring the same collector twice.
	Endpoint string

	// Insecure sends plain HTTP. A collector inside the same cluster commonly
	// has no certificate, and requiring one there would push people to disable
	// telemetry rather than to issue one.
	Insecure bool

	// Interval is how often metrics are pushed while the process runs. It
	// matters less than it looks: a fetcher usually exits before the first
	// tick, and what actually delivers is Close.
	Interval time.Duration

	// Service names this process to the collector. Empty uses the pipeline's
	// name, which the SDK already puts on every metric as a label.
	Service string
}

// New builds a Meter. It returns an error rather than a working-but-silent
// object: a telemetry setup that fails to connect and reports success is how a
// team finds out months later that a dashboard was empty the whole time.
func New(ctx context.Context, opts Options) (*Meter, error) {
	exporterOptions := []otlpmetrichttp.Option{}
	if opts.Endpoint != "" {
		exporterOptions = append(exporterOptions, otlpmetrichttp.WithEndpoint(opts.Endpoint))
	}
	if opts.Insecure {
		exporterOptions = append(exporterOptions, otlpmetrichttp.WithInsecure())
	}

	exporter, err := otlpmetrichttp.New(ctx, exporterOptions...)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(interval))),
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{
				Name: "*_seconds",
				Kind: sdkmetric.InstrumentKindHistogram,
			},
			sdkmetric.Stream{
				Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
					Boundaries: secondsBuckets,
				},
			},
		)),
	)

	service := opts.Service
	if service == "" {
		service = "brevis-sdk"
	}
	return &Meter{
		provider:   provider,
		meter:      provider.Meter(service),
		counters:   map[string]api.Int64Counter{},
		histograms: map[string]api.Float64Histogram{},
	}, nil
}

// Counter implements sdk.Meter.
func (m *Meter) Counter(name string, value int64, attrs ...sdk.Attr) {
	m.mu.Lock()
	c, ok := m.counters[name]
	if !ok {
		var err error
		if c, err = m.meter.Int64Counter(name); err != nil {
			// An instrument that will not register is a bad name, which is a
			// programming error in the fetcher. It must not take the pipeline
			// down: the load already happened, and losing a counter is not a
			// reason to fail a run that wrote its rows.
			m.counters[name] = nil
			m.mu.Unlock()
			return
		}
		m.counters[name] = c
	}
	m.mu.Unlock()
	if c == nil {
		return
	}
	c.Add(context.Background(), value, api.WithAttributes(convert(attrs)...))
}

// Histogram implements sdk.Meter.
func (m *Meter) Histogram(name string, value float64, attrs ...sdk.Attr) {
	m.mu.Lock()
	h, ok := m.histograms[name]
	if !ok {
		var err error
		if h, err = m.meter.Float64Histogram(name); err != nil {
			m.histograms[name] = nil
			m.mu.Unlock()
			return
		}
		m.histograms[name] = h
	}
	m.mu.Unlock()
	if h == nil {
		return
	}
	h.Record(context.Background(), value, api.WithAttributes(convert(attrs)...))
}

// Close flushes and shuts the exporter down. THIS is the call that makes push
// work for a task pod, and skipping it is how a fetcher reports nothing.
//
// The periodic reader's interval is thirty seconds; a fetcher that runs for
// twelve exits before the first tick, and every number it recorded dies with
// the process. Shutdown flushes what is pending before it returns.
//
//	m, err := otelmeter.New(ctx, otelmeter.Options{Insecure: true})
//	if err != nil { return err }
//	defer func() { _ = m.Close(context.Background()) }()
//
// The deferred context is a fresh one on purpose: the run's context is usually
// already cancelled by the time the deferred call runs, and passing it would
// abort the flush at the exact moment it matters.
func (m *Meter) Close(ctx context.Context) error {
	if m == nil || m.provider == nil {
		return nil
	}
	// A ceiling of its own: a collector that is down must delay the pod's exit
	// by seconds, not hold a Job open until Kubernetes kills it.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.provider.Shutdown(ctx)
}

func convert(attrs []sdk.Attr) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, attribute.String(a.Key, a.Value))
	}
	return out
}

// Meter satisfies sdk.Meter. Asserted here so a change to the interface breaks
// the build rather than a consumer's.
var _ sdk.Meter = (*Meter)(nil)
