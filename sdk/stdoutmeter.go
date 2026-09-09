package sdk

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// StdoutMeter reports through the pipe the engine already reads.
//
//	sdk.Run(sdk.Pipeline{..., Meter: sdk.StdoutMeter{}})
//
// It exists because `Meter` needed an implementation that costs nothing and
// requires no configuration. Before it, a fetcher that counted something
// published it NOWHERE unless the consumer imported sdk/metrics/otelmeter and
// ran a collector -- so the interface was there and the numbers were not.
//
// # Why stdout and not a port
//
// A step is not scrapeable. It runs in its own pod for forty seconds and exits;
// a port it opened would be scraped never, or once by luck. The engine already
// reads every step's stdout looking for `@brevis:` lines -- that is how this
// SDK's phases reach the graph -- and it already has a meter and a Prometheus
// endpoint. So the step declares and the ENGINE carries.
//
// The metric arrives labelled with the workflow and the step, supplied by the
// engine. A step cannot label itself correctly: it does not know its own slug,
// and asking every language's library for it would get it wrong the first time
// somebody renamed a file.
//
// # Outside the engine
//
// The line still goes to stdout, where it reads as what it is. Nothing collects
// it and nothing fails -- the same as a phase announced by a fetcher somebody
// runs by hand.
type StdoutMeter struct {
	// Out is where the lines go. Nil means os.Stdout, which is the only
	// destination the engine reads; the field exists for tests.
	Out interface{ Write([]byte) (int, error) }

	mu      sync.Mutex
	refused map[string]bool
}

// Prometheus's own rule. A name that breaks it does not lose one series -- it
// makes Prometheus refuse the WHOLE scrape. Refused here, where the mistake was
// made, rather than renamed into something nobody wrote.
var metricName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Counter satisfies Meter.
func (m *StdoutMeter) Counter(name string, value int64, attrs ...Attr) {
	m.emit(name, "counter", float64(value), attrs)
}

// Histogram satisfies Meter.
//
// It is reported as a GAUGE, and the difference is worth stating rather than
// hiding. A histogram needs buckets, and buckets chosen per step by whoever
// wrote the step is a cardinality decision taken in the wrong place -- see
// docs/plan/2026-09-09-step-metrics.md §4. The last observation is what
// survives, which for a fetcher's duration is the useful number and for a true
// distribution is not: that one wants sdk/metrics/otelmeter and a collector.
func (m *StdoutMeter) Histogram(name string, value float64, attrs ...Attr) {
	m.emit(name, "gauge", value, attrs)
}

func (m *StdoutMeter) emit(name, kind string, value float64, attrs []Attr) {
	if !metricName.MatchString(name) {
		m.warnOnce(name)
		return
	}
	line, err := json.Marshal(map[string]any{
		"type":  "metric",
		"name":  name,
		"kind":  kind,
		"value": value,
		"at":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}

	out := m.Out
	if out == nil {
		out = os.Stdout
	}
	// One Write of the whole line, newline included. Two writes let a
	// concurrent transform interleave its own output in the middle of the
	// marker, and half a marker is a log line nobody can read.
	_, _ = out.Write(append(append([]byte("@brevis:"), line...), '\n'))

	// The attributes are dropped, and saying so is better than pretending.
	// The engine supplies workflow and step, and a step that could add its own
	// would add a customer id on the first Tuesday -- which is how a metrics
	// backend becomes the most expensive part of an installation.
	_ = attrs
}

// warnOnce complains about a bad name a single time.
//
// A fetcher in a loop would otherwise write this on every iteration, and the
// log is precisely what has to keep working when something is wrong.
func (m *StdoutMeter) warnOnce(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refused == nil {
		m.refused = map[string]bool{}
	}
	if m.refused[name] {
		return
	}
	m.refused[name] = true
	fmt.Fprintf(os.Stderr,
		"brevis: %q is not a valid metric name and was dropped. Prometheus accepts "+
			"letters, digits and underscore, and the first character cannot be a digit -- "+
			"so `rows_loaded` and not %q. A name it refuses costs the whole scrape.\n",
		name, strings.TrimSpace(name))
}
