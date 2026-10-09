package serve

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// The exposition is written BY HAND, and the number is why.
//
// `prometheus/client_golang` in this binary was measured on 2026-10-09:
//
//	today                 252 packages, 11 MB
//	with promhttp         303 packages, 19 MB
//	the ceilings          280 packages, 16 MB
//
// It busts both. This module's whole premise is weight -- it refused the
// official BigQuery client for 31 MB and wrote two hundred lines of REST
// instead -- and four counters are not the place to spend 51 packages and
// 8 MB. The text format is specified and short; what it costs is this file,
// and a test that asserts every series has its HELP and TYPE.
//
// WHAT IS NOT HERE IS THE POINT. No SQL, and no statement hash: the audit
// line carries one because it is a LOG with a retention, and a label is
// forever. A time series database keeps one series per distinct label value,
// so a hash here would be a series per query -- a leak and an outage in the
// same decision.
type metrics struct {
	mu sync.Mutex

	// queries is endpoint+outcome -> count. Bounded by two endpoints and the
	// handful of outcomes an audit line can carry.
	queries map[[2]string]int64

	// scanned is connection -> bytes. Bounded by the registry: a connection
	// nobody declared cannot be opened.
	scanned map[string]int64

	// returned and millis are per endpoint, as a sum and a count, which is
	// what a summary without quantiles is. Quantiles need reservoirs and
	// reservoirs need the library this file exists to avoid.
	returned map[string]int64
	millis   map[string]int64
	calls    map[string]int64
}

func newMetrics() *metrics {
	return &metrics{
		queries:  map[[2]string]int64{},
		scanned:  map[string]int64{},
		returned: map[string]int64{},
		millis:   map[string]int64{},
		calls:    map[string]int64{},
	}
}

// observe records one answered request. It takes the same record the audit
// line does, so the two cannot disagree about what happened.
func (m *metrics) observe(endpoint string, line record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries[[2]string{endpoint, line.Outcome}]++
	m.calls[endpoint]++
	m.millis[endpoint] += line.Millis
	m.returned[endpoint] += int64(line.Returned)
	if line.Connection != "" && line.Bytes > 0 {
		m.scanned[line.Connection] += line.Bytes
	}
}

// Metrics is the exposition, on a listener of its own. See Service.Metrics.
func (m *metrics) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder

		m.mu.Lock()
		defer m.mu.Unlock()

		b.WriteString("# HELP brevis_sql_queries_total Requests answered, by endpoint and outcome.\n")
		b.WriteString("# TYPE brevis_sql_queries_total counter\n")
		for _, k := range sortedPairs(m.queries) {
			fmt.Fprintf(&b, "brevis_sql_queries_total{endpoint=%q,outcome=%q} %d\n",
				k[0], k[1], m.queries[k])
		}

		b.WriteString("# HELP brevis_sql_scanned_bytes_total Bytes the warehouse reported scanning.\n")
		b.WriteString("# TYPE brevis_sql_scanned_bytes_total counter\n")
		for _, c := range sortedKeys(m.scanned) {
			fmt.Fprintf(&b, "brevis_sql_scanned_bytes_total{connection=%q} %d\n", c, m.scanned[c])
		}

		b.WriteString("# HELP brevis_sql_rows_returned_total Rows drawn, after the ceiling cut.\n")
		b.WriteString("# TYPE brevis_sql_rows_returned_total counter\n")
		for _, e := range sortedKeys(m.returned) {
			fmt.Fprintf(&b, "brevis_sql_rows_returned_total{endpoint=%q} %d\n", e, m.returned[e])
		}

		b.WriteString("# HELP brevis_sql_duration_seconds How long requests took.\n")
		b.WriteString("# TYPE brevis_sql_duration_seconds summary\n")
		for _, e := range sortedKeys(m.calls) {
			fmt.Fprintf(&b, "brevis_sql_duration_seconds_sum{endpoint=%q} %.3f\n",
				e, float64(m.millis[e])/1000)
			fmt.Fprintf(&b, "brevis_sql_duration_seconds_count{endpoint=%q} %d\n", e, m.calls[e])
		}

		_, _ = w.Write([]byte(b.String()))
	})
}

// Sorted so a scrape reads the same twice and a diff of two scrapes is about
// the numbers. A map's order is not.
func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedPairs(m map[[2]string]int64) [][2]string {
	out := make([][2]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
