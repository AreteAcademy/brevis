package gateway

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ContentType is what a scraper expects. Version 0.0.4 is the text format
// Prometheus has served and parsed since 2014; OpenMetrics is a superset and
// scrapers accept this.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Render writes the current values in exposition format.
//
// Not `WriteTo`: that name belongs to io.WriterTo, whose signature returns the
// byte count, and a method that looks like an interface it does not implement
// is one somebody passes where the interface is expected.
//
// A nil receiver writes nothing and reports no error: the endpoint answers 200
// with an empty body, which is a valid scrape of a process that has no metrics.
// A 404 or a panic would make "not configured" look like "broken".
func (m *Metrics) Render(w io.Writer) error {
	if m == nil {
		return nil
	}
	for _, c := range []*counters{
		m.received, m.rejected, m.dropped, m.batches, m.buried, m.saturated, m.oversized,
		m.flushes, m.ingestedBytes, m.ingestedEvents,
	} {
		if err := c.render(w); err != nil {
			return err
		}
	}
	for _, h := range []*histograms{m.delivery, m.batchSize} {
		if err := h.render(w); err != nil {
			return err
		}
	}
	if m.depth == nil {
		return nil
	}
	// The gauges are read HERE, at scrape time, straight off the pipes. A
	// buffer depth recorded on every event would be a write per event to
	// report a number nobody reads between scrapes.
	byName := map[string][]gauge{}
	var names []string
	for _, g := range m.depth() {
		if _, seen := byName[g.name]; !seen {
			names = append(names, g.name)
		}
		byName[g.name] = append(byName[g.name], g)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := fmt.Fprintf(w, "# TYPE %s gauge\n", n); err != nil {
			return err
		}
		for _, g := range byName[n] {
			if _, err := fmt.Fprintf(w, "%s{stream=%q} %d\n", n, escape(g.labels[0]), g.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *counters) render(w io.Writer) error {
	// Nil is an instrument that was never built, which is how the volume pair
	// is off when BREVIS_INGESTION_METRICS is unset. It renders nothing, for
	// the same reason a nil *Metrics does: "not configured" must not look like
	// "broken".
	if c == nil {
		return nil
	}
	c.mu.RLock()
	keys := make([]string, 0, len(c.by))
	values := make(map[string]int64, len(c.by))
	for k, v := range c.by {
		keys = append(keys, k)
		values[k] = v.Load()
	}
	c.mu.RUnlock()

	if len(keys) == 0 {
		// A series nobody has incremented is left out rather than written as
		// zero. Prometheus handles an absent series; what it cannot do is tell
		// a real zero from a placeholder.
		return nil
	}
	sort.Strings(keys)

	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name); err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s%s %d\n", c.name, labelsOf(c.labelNames, k), values[k]); err != nil {
			return err
		}
	}
	return nil
}

func (h *histograms) render(w io.Writer) error {
	h.mu.Lock()
	type snap struct {
		key    string
		counts []uint64
		sum    float64
		total  uint64
	}
	snaps := make([]snap, 0, len(h.by))
	for k, b := range h.by {
		counts := append([]uint64(nil), b.counts...)
		snaps = append(snaps, snap{key: k, counts: counts, sum: b.sum, total: b.total})
	}
	h.mu.Unlock()

	if len(snaps) == 0 {
		return nil
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].key < snaps[j].key })

	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name); err != nil {
		return err
	}
	for _, s := range snaps {
		base := labelsOf(h.labelNames, s.key)
		// RUNNING TOTALS. This format's le="0.5" means "how many were at most
		// 0.5", not "how many were in this bucket" -- which is the opposite of
		// how OpenTelemetry holds them, and the mistake produces a chart that
		// looks plausible and is wrong.
		var running uint64
		for i, bound := range h.bounds {
			running += s.counts[i]
			if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name,
				withLE(base, strconv.FormatFloat(bound, 'g', -1, 64)), running); err != nil {
				return err
			}
		}
		running += s.counts[len(h.bounds)]
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, withLE(base, "+Inf"), running); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_sum%s %s\n%s_count%s %d\n",
			h.name, base, strconv.FormatFloat(s.sum, 'g', -1, 64),
			h.name, base, s.total); err != nil {
			return err
		}
	}
	return nil
}

// labelsOf renders `{a="1",b="2"}` from the packed key. Empty when there are
// no labels, because `name{} 1` is not valid.
func labelsOf(names []string, key string) string {
	if len(names) == 0 {
		return ""
	}
	values := strings.Split(key, "\x00")
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		v := ""
		if i < len(values) {
			v = values[i]
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escape(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// withLE inserts the bucket bound into an existing label set.
func withLE(labels, le string) string {
	if labels == "" {
		return `{le="` + le + `"}`
	}
	return labels[:len(labels)-1] + `,le="` + le + `"}`
}

// escape makes a value safe inside quotes.
//
// A backslash, a quote or a newline in a label ends the series early and
// corrupts every line after it -- and a scraper reading truncated text does not
// error, it records what it managed to parse, which looks like data.
func escape(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// MetricsHandler serves the exposition.
//
// It is NEVER on the ingest mux, and the rule matters more here than it does
// in the engine: the gateway's port is public by design -- it is where clients
// POST -- so a /metrics on it would publish every stream name, path and
// destination to whoever finds the path.
func (m *Metrics) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Buffered, so a failure mid-render does not leave a half-written 200
		// on the wire.
		var body strings.Builder
		if err := m.Render(&body); err != nil {
			http.Error(w, "collecting metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body.String())
	})
}
