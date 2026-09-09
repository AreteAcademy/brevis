package sdk_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
)

type buffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *buffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *buffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestTheStdoutMeterWritesWhatTheEngineParses.
//
// The line is a CONTRACT with internal/application/execution/stages.go, and the
// two live in different modules -- nothing but a test naming the fields would
// notice them drifting apart.
func TestTheStdoutMeterWritesWhatTheEngineParses(t *testing.T) {
	var out buffer
	m := &sdk.StdoutMeter{Out: &out}
	m.Counter("rows_loaded", 48213)

	line := strings.TrimSpace(out.String())
	if !strings.HasPrefix(line, "@brevis:") {
		t.Fatalf("the line does not carry the marker: %q", line)
	}
	for _, want := range []string{
		`"type":"metric"`, `"name":"rows_loaded"`, `"kind":"counter"`, `"value":48213`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the line does not carry %s: %s", want, line)
		}
	}
}

// One Write per line, newline included.
//
// Two writes let a concurrent transform interleave its own output in the middle
// of the marker, and half a marker is a log line nobody can read -- and one the
// engine hands back to the log rather than consuming.
func TestEachLineIsASingleWrite(t *testing.T) {
	var w countingWriter
	m := &sdk.StdoutMeter{Out: &w}
	m.Counter("a_total", 1)
	m.Histogram("b_seconds", 1.5)

	if w.writes != 2 {
		t.Errorf("%d writes for two metrics", w.writes)
	}
	if got := strings.Count(w.text, "\n"); got != 2 {
		t.Errorf("%d newlines for two metrics", got)
	}
}

type countingWriter struct {
	writes int
	text   string
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.text += string(p)
	return len(p), nil
}

// A name Prometheus cannot parse never reaches the pipe. It does not lose one
// series -- it costs the whole scrape.
func TestAnInvalidNameNeverReachesThePipe(t *testing.T) {
	var out buffer
	m := &sdk.StdoutMeter{Out: &out}
	for _, bad := range []string{"rows-loaded", "rows loaded", "2rows", "", `a"b`} {
		m.Counter(bad, 1)
	}
	if got := out.String(); got != "" {
		t.Errorf("an invalid name was written: %q", got)
	}
	// And a valid one still works afterwards: the refusal is per name, not a
	// latch that turns the meter off.
	m.Counter("good_total", 1)
	if !strings.Contains(out.String(), "good_total") {
		t.Error("the meter stopped working after refusing a name")
	}
}

// The Meter interface is satisfied, which is what lets a Pipeline take one.
func TestTheStdoutMeterIsAMeter(t *testing.T) {
	var _ sdk.Meter = &sdk.StdoutMeter{}
}

// A histogram is reported as a gauge, and the test says so rather than letting
// somebody discover it from a dashboard. Buckets chosen per step by whoever
// wrote the step is a cardinality decision taken in the wrong place.
func TestAHistogramIsReportedAsAGauge(t *testing.T) {
	var out buffer
	m := &sdk.StdoutMeter{Out: &out}
	m.Histogram("extract_seconds", 2.5)
	if !strings.Contains(out.String(), `"kind":"gauge"`) {
		t.Errorf("histogram wrote %q", out.String())
	}
}
