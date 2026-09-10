package metrics

// The Prometheus text exposition format, written here rather than imported.
//
// The trade is stated in docs/OBSERVABILITY.md:
// go.opentelemetry.io/otel/exporters/prometheus costs 49 packages, 29 of them
// google.golang.org/protobuf, to produce the text below. The format is
// `name{label="value"} 42` and it has not changed in a decade.
//
// Two things are easy to get wrong here, and both have a test of their own:
//
//   - OpenTelemetry counts a histogram's buckets PER BUCKET; Prometheus reads
//     them as RUNNING TOTALS. Getting this backwards produces a chart that looks
//     plausible and is wrong, which is worse than one that breaks.
//   - Label values are quoted, so a backslash, a quote or a newline inside one
//     ends the series early and corrupts every line after it.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// ContentType is what a scraper expects. Version 0.0.4 is the text format
// Prometheus has served and parsed since 2014; OpenMetrics is a superset and
// scrapers accept this.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// WriteTo collects the current values and writes them in exposition format.
//
// A nil receiver writes nothing and reports no error: the endpoint answers 200
// with an empty body, which is a valid scrape of a process that has no metrics.
// Answering 404 or panicking would make "not configured" look like "broken".
func (m *Metrics) WriteTo(ctx context.Context, w io.Writer) error {
	if m == nil || m.reader == nil {
		return nil
	}
	var rm metricdata.ResourceMetrics
	if err := m.reader.Collect(ctx, &rm); err != nil {
		return err
	}
	return write(w, &rm)
}

// family is one metric name with every series under it. Grouping by name is
// required, not cosmetic: Prometheus rejects a scrape where a name's series are
// interleaved with another's, and the SDK groups by instrumentation scope.
type family struct {
	name, help, kind string
	points           []point
}

// point is one data point's lines, kept together and keyed for sorting.
//
// The lines are NOT sorted individually, and that is the whole reason this type
// exists. A histogram's bucket lines differ only in `le`, whose values are
// numbers in a string: sorting text puts `le="+Inf"` first and `le="5"` after
// `le="3600"`. Generating them in numeric order and never resorting keeps the
// output both deterministic and readable as a histogram.
type point struct {
	key   string
	lines []string
}

// counted wraps the writer and keeps the FIRST error, so a failed write stops
// producing output instead of finishing the render and reporting success. It is
// also why the Fprint calls below discard their error: it is the same one, kept
// here, and checked once per family rather than on every line.
// Without it, a writer that fails halfway leaves a truncated exposition and a
// nil error -- and a scraper reading truncated text does not error, it records
// the series it managed to parse.
type counted struct {
	w   io.Writer
	err error
}

func (c *counted) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.err = err
	return n, err
}

func write(dst io.Writer, rm *metricdata.ResourceMetrics) error {
	w := &counted{w: dst}
	byName := map[string]*family{}
	var order []string

	for _, scope := range rm.ScopeMetrics {
		for _, md := range scope.Metrics {
			f, ok := byName[md.Name]
			if !ok {
				f = &family{name: md.Name, help: md.Description}
				byName[md.Name] = f
				order = append(order, md.Name)
			}
			render(f, md)
		}
	}

	sort.Strings(order)
	for _, name := range order {
		f := byName[name]
		if len(f.points) == 0 {
			// A metric with no data points is a metric nobody has touched. It
			// is left out entirely rather than emitted with a zero: the rule
			// this repository states is that a number always zero is worse than
			// no number, and an absent series is what a scraper can tell apart.
			continue
		}
		if f.help != "" {
			_, _ = fmt.Fprintf(w, "# HELP %s %s\n", f.name, escapeHelp(f.help))
		}
		if f.kind != "" {
			_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", f.name, f.kind)
		}
		// Sorted by data point, so the same state produces the same bytes. A
		// golden test on this output is the reason for owning the format, and
		// map iteration order in the SDK would take it away.
		sort.Slice(f.points, func(i, j int) bool { return f.points[i].key < f.points[j].key })
		for _, p := range f.points {
			for _, line := range p.lines {
				_, _ = fmt.Fprintln(w, line)
			}
		}
		if w.err != nil {
			return w.err
		}
	}
	return w.err
}

func render(f *family, md metricdata.Metrics) {
	switch d := md.Data.(type) {
	case metricdata.Sum[int64]:
		f.kind = sumKind(d.IsMonotonic)
		for _, p := range d.DataPoints {
			f.add(p.Attributes, series(md.Name, p.Attributes, nil, strconv.FormatInt(p.Value, 10)))
		}
	case metricdata.Sum[float64]:
		f.kind = sumKind(d.IsMonotonic)
		for _, p := range d.DataPoints {
			f.add(p.Attributes, series(md.Name, p.Attributes, nil, number(p.Value)))
		}
	case metricdata.Gauge[int64]:
		f.kind = "gauge"
		for _, p := range d.DataPoints {
			f.add(p.Attributes, series(md.Name, p.Attributes, nil, strconv.FormatInt(p.Value, 10)))
		}
	case metricdata.Gauge[float64]:
		f.kind = "gauge"
		for _, p := range d.DataPoints {
			f.add(p.Attributes, series(md.Name, p.Attributes, nil, number(p.Value)))
		}
	case metricdata.Histogram[int64]:
		f.kind = "histogram"
		for _, p := range d.DataPoints {
			f.add(p.Attributes, histogram(md.Name, p.Attributes, p.Bounds, p.BucketCounts, p.Count, float64(p.Sum))...)
		}
	case metricdata.Histogram[float64]:
		f.kind = "histogram"
		for _, p := range d.DataPoints {
			f.add(p.Attributes, histogram(md.Name, p.Attributes, p.Bounds, p.BucketCounts, p.Count, p.Sum)...)
		}
	}
	// Anything else -- exponential histograms, summaries -- is skipped rather
	// than guessed at. Nothing in this engine creates one, and a wrong
	// rendering would be discovered by a chart, not by a build.
}

// add files one data point's lines under a sort key built from its attributes.
func (f *family) add(attrs attribute.Set, lines ...string) {
	f.points = append(f.points, point{key: attrs.Encoded(attribute.DefaultEncoder()), lines: lines})
}

// sumKind: a non-monotonic sum is an UpDownCounter, which has no Prometheus
// equivalent and is conventionally exposed as a gauge.
func sumKind(monotonic bool) string {
	if monotonic {
		return "counter"
	}
	return "gauge"
}

// histogram returns the four kinds of line a Prometheus histogram is made of,
// in the order they have to be read.
//
// The cumulative conversion is here, and it is the reason this function exists
// separately: OpenTelemetry's BucketCounts[i] is how many observations fell in
// bucket i, while Prometheus's `le` series is how many fell at or below that
// bound. Running total, not the raw count.
func histogram(name string, attrs attribute.Set,
	bounds []float64, counts []uint64, total uint64, sum float64,
) []string {
	lines := make([]string, 0, len(bounds)+3)
	var running uint64
	for i, bound := range bounds {
		if i < len(counts) {
			running += counts[i]
		}
		lines = append(lines, series(name+"_bucket", attrs,
			&label{"le", number(bound)}, strconv.FormatUint(running, 10)))
	}
	// The overflow bucket. It is not optional: a Prometheus histogram without
	// +Inf is rejected, and it must equal _count.
	lines = append(lines, series(name+"_bucket", attrs,
		&label{"le", "+Inf"}, strconv.FormatUint(total, 10)))
	lines = append(lines, series(name+"_sum", attrs, nil, number(sum)))
	lines = append(lines, series(name+"_count", attrs, nil, strconv.FormatUint(total, 10)))
	return lines
}

// label is one name=value pair. It exists so the bucket bound can be appended
// after the attribute set without a second code path.
type label struct{ name, value string }

// series renders one line. `extra` is the bucket bound, which has to come last
// among the labels for the sorted output to read the way a human expects.
//
// The quoting is written out rather than done with %q, and that is not style:
// %q escapes a Go string literal, so applying it after escapeValue would double
// every backslash that escapeValue had just added.
func series(name string, attrs attribute.Set, extra *label, value string) string {
	var b strings.Builder
	b.WriteString(name)

	pairs := make([]string, 0, attrs.Len()+1)
	for it := attrs.Iter(); it.Next(); {
		kv := it.Attribute()
		pairs = append(pairs, string(kv.Key)+`="`+escapeValue(kv.Value.String())+`"`)
	}
	sort.Strings(pairs)
	if extra != nil {
		pairs = append(pairs, extra.name+`="`+escapeValue(extra.value)+`"`)
	}

	if len(pairs) > 0 {
		b.WriteString("{")
		b.WriteString(strings.Join(pairs, ","))
		b.WriteString("}")
	}
	b.WriteString(" ")
	b.WriteString(value)
	return b.String()
}

// escapeValue handles the three characters that break a quoted label value.
// The order matters: backslashes first, or the escapes added below get escaped
// again.
func escapeValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// escapeHelp is a shorter list: HELP text runs to the end of the line and is
// not quoted, so a quote inside it is fine and a newline is not.
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// number formats a float the way the exposition format expects, including the
// three special values that have names rather than digits.
func number(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
