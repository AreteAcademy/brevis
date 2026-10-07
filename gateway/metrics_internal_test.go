package gateway

import (
	"strings"
	"testing"
)

// The two mistakes the engine's own exposition names, each with a test,
// because both produce output a scraper accepts and reads wrong.

// A histogram's buckets are RUNNING TOTALS in this format: le="0.5" means "how
// many were at most 0.5", not "how many landed in this bucket".
//
// Backwards, it produces a chart that looks plausible and is wrong -- which is
// worse than one that breaks, because nobody goes looking.
func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := NewMetrics()
	// Three observations that land in three different buckets.
	m.observe(m.delivery, 0.003, "clicks", "sink") // <= 0.005
	m.observe(m.delivery, 0.03, "clicks", "sink")  // <= 0.05
	m.observe(m.delivery, 7, "clicks", "sink")     // <= 10

	text := renderInternal(t, m)
	for _, want := range []string{
		`brevis_gateway_delivery_seconds_bucket{stream="clicks",sink="sink",le="0.005"} 1`,
		`brevis_gateway_delivery_seconds_bucket{stream="clicks",sink="sink",le="0.05"} 2`,
		`brevis_gateway_delivery_seconds_bucket{stream="clicks",sink="sink",le="10"} 3`,
		`brevis_gateway_delivery_seconds_bucket{stream="clicks",sink="sink",le="+Inf"} 3`,
		`brevis_gateway_delivery_seconds_count{stream="clicks",sink="sink"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// And the one that proves they are cumulative rather than per-bucket: the
	// middle bucket holds ONE observation, and must report two.
	if strings.Contains(text, `le="0.05"} 1`) {
		t.Error("the buckets are per-bucket counts, not running totals")
	}
}

// A label value is quoted, so a quote, a backslash or a newline inside one ends
// the series early and corrupts every line after it.
//
// And a scraper reading truncated text does NOT error -- it records the series
// it managed to parse, which is worse than nothing because it looks like data.
func TestALabelValueCannotBreakTheExposition(t *testing.T) {
	m := NewMetrics()
	m.count(m.saturated, 1, `a"b\c`+"\nd")

	text := renderInternal(t, m)
	if !strings.Contains(text, `brevis_gateway_saturated_total{stream="a\"b\\c\nd"} 1`) {
		t.Errorf("the value was not escaped:\n%s", text)
	}
	// Every line has to still be one line.
	//
	// The check is on LABELLED series, because not every line has labels:
	// `process_start_time_seconds` is a bare gauge with a well-known name and
	// no labels at all, so requiring a `}` here would fail on a line that
	// cannot carry a label value and therefore cannot leak one.
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if !strings.Contains(line, "{") {
			continue // an unlabelled series; nothing to escape
		}
		if !strings.Contains(line, "} ") {
			t.Errorf("a line lost its shape, so the escaping leaked: %q", line)
		}
	}
}

func renderInternal(t *testing.T, m *Metrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
