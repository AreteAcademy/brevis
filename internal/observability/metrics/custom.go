package metrics

import (
	"context"
	"log/slog"
	"regexp"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

// customCeiling is how many DISTINCT metric names one process will register
// from steps.
//
// The name comes down a pipe from somebody else's code, and
// `metrics.set(f"rows_{customer}", n)` in a loop grows this registry until the
// process dies -- in the SCHEDULER, taking the runs with it. That is the same
// reasoning as stageCeiling in the execution package, and the same conclusion:
// this is the cap of somebody who does not trust what came down the pipe.
//
// It caps NAMES, not writes. A name already registered costs nothing to write
// again, so a step reporting one number every second is free.
const customCeiling = 200

// validName is Prometheus's own rule.
//
// A name that breaks it does not produce a broken metric -- it produces an
// exposition Prometheus REFUSES TO PARSE, and the entire scrape is lost, every
// other metric with it. The SDKs refuse such a name before it is ever written,
// and this is the second line of defence for anything that reaches the pipe by
// another route.
var validName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// custom holds the instruments a step asked for.
type custom struct {
	mu       sync.Mutex
	counters map[string]api.Int64Counter
	gauges   map[string]api.Float64Gauge
	// refused remembers the names already turned away, so a step in a loop
	// produces one log line rather than one per second.
	refused map[string]bool
	full    bool
}

// StepMetric records one value a step reported through its stdout.
//
// `kind` is "gauge" (last value wins) or "counter" (adds up). Anything else is
// ignored: an unknown kind is a version of the SDK this engine does not know,
// and inventing a meaning for it would put a number on a dashboard that means
// something else.
//
// The labels are the ENGINE's -- workflow and step -- and never the step's own.
// A step that could add labels would add `customer_id` on the first Tuesday, and
// the run id is deliberately absent here for the reason it is absent
// everywhere: one unbounded label is how a metrics backend falls over.
func (m *Metrics) StepMetric(ctx context.Context, workflow, step, name, kind string, value float64, log *slog.Logger) {
	if m == nil || m.meter == nil {
		return
	}
	if !validName.MatchString(name) {
		m.refuse(name, "the name is not a valid Prometheus metric name", log)
		return
	}

	attrs := api.WithAttributes(
		attribute.String("workflow", workflow),
		attribute.String("step", step),
	)

	switch kind {
	case "counter":
		if c := m.counter(name, log); c != nil {
			c.Add(ctx, int64(value), attrs)
		}
	case "gauge", "":
		if g := m.gauge(name, log); g != nil {
			g.Record(ctx, value, attrs)
		}
	default:
		m.refuse(name, "unknown kind "+kind, log)
	}
}

// The two instrument caches. Both take the same lock: the maps are written on
// the FIRST use of a name and read on every one after, and a step reporting a
// number every second must not serialise on a mutex it does not need -- but a
// hundred steps registering at once must not race either. First-use writes are
// rare enough that one lock is the simple answer.
func (m *Metrics) counter(name string, log *slog.Logger) api.Int64Counter {
	m.custom.mu.Lock()
	defer m.custom.mu.Unlock()
	if c, ok := m.custom.counters[name]; ok {
		return c
	}
	if !m.room(name, log) {
		return nil
	}
	c, err := m.meter.Int64Counter(prefix+name,
		api.WithDescription("Reported by a step through the @brevis: protocol."))
	if err != nil {
		m.custom.refused[name] = true
		log.Warn("a step's metric could not be registered", "metric", name, "error", err)
		return nil
	}
	if m.custom.counters == nil {
		m.custom.counters = map[string]api.Int64Counter{}
	}
	m.custom.counters[name] = c
	return c
}

func (m *Metrics) gauge(name string, log *slog.Logger) api.Float64Gauge {
	m.custom.mu.Lock()
	defer m.custom.mu.Unlock()
	if g, ok := m.custom.gauges[name]; ok {
		return g
	}
	if !m.room(name, log) {
		return nil
	}
	g, err := m.meter.Float64Gauge(prefix+name,
		api.WithDescription("Reported by a step through the @brevis: protocol."))
	if err != nil {
		m.custom.refused[name] = true
		log.Warn("a step's metric could not be registered", "metric", name, "error", err)
		return nil
	}
	if m.custom.gauges == nil {
		m.custom.gauges = map[string]api.Float64Gauge{}
	}
	m.custom.gauges[name] = g
	return g
}

// room answers whether another NAME fits, and says so once when it does not.
// Called with the lock held.
func (m *Metrics) room(name string, log *slog.Logger) bool {
	if len(m.custom.counters)+len(m.custom.gauges) < customCeiling {
		return true
	}
	if !m.custom.full {
		m.custom.full = true
		// Once. A pipeline generating names in a loop would otherwise write
		// this line as fast as it can run, and the log is precisely what must
		// keep working when something is wrong.
		log.Warn("the step-metric ceiling is full; further NAMES are dropped",
			"ceiling", customCeiling, "first_dropped", name)
	}
	return false
}

// refuse logs a bad name once and remembers it. Takes the lock itself.
func (m *Metrics) refuse(name, why string, log *slog.Logger) {
	m.custom.mu.Lock()
	defer m.custom.mu.Unlock()
	if m.custom.refused == nil {
		m.custom.refused = map[string]bool{}
	}
	if m.custom.refused[name] {
		return
	}
	m.custom.refused[name] = true
	log.Warn("a step's metric was refused", "metric", name, "reason", why)
}

// prefix keeps a step's metric from colliding with the engine's own.
//
// Without it a step could declare `brevis_run_total` and its numbers would be
// added to the engine's, which is a dashboard that lies rather than one that is
// missing something.
const prefix = "brevis_step_"
