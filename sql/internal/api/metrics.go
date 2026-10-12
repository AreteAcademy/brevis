package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// The exposition is written BY HAND, for `serve`'s reason, measured there:
// `prometheus/client_golang` is 51 packages and 8 MB over both of this
// module's ceilings, and five series are not the place to spend them.
//
// NO TENANT IN ANY LABEL, NO VALUE ANYWHERE. A label is a series for as long
// as the database keeps it: one per tenant is a cardinality problem and a
// customer list on a port. Paths are not labels either -- an unauthorised
// caller chooses the path, and a label from it would be a series per guess.
// Endpoints are labels because the file bounds them.
type metrics struct {
	mu        sync.Mutex
	requests  map[[2]string]int64 // endpoint, status
	rows      map[string]int64
	billed    map[string]int64
	request   map[string]*histogram
	warehouse map[string]*histogram
	cache     map[[2]string]int64 // endpoint, result
}

// buckets are seconds, from a cache hit to the default timeout.
var buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	counts []int64
	sum    float64
	n      int64
}

func (h *histogram) observe(seconds float64) {
	if h.counts == nil {
		h.counts = make([]int64, len(buckets))
	}
	for i, le := range buckets {
		if seconds <= le {
			h.counts[i]++
		}
	}
	h.sum += seconds
	h.n++
}

func newMetrics() *metrics {
	return &metrics{
		requests: map[[2]string]int64{}, rows: map[string]int64{}, billed: map[string]int64{},
		request: map[string]*histogram{}, warehouse: map[string]*histogram{},
		cache: map[[2]string]int64{},
	}
}

// noEndpoint is the label a request carries when it reached no endpoint.
const noEndpoint = "-"

func (m *metrics) observe(line *record, took, warehouse time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[[2]string{line.Endpoint, strconv.Itoa(line.Status)}]++
	if line.Endpoint == noEndpoint {
		return
	}
	if line.Cache != "" {
		m.cache[[2]string{line.Endpoint, line.Cache}]++
	}
	m.rows[line.Endpoint] += int64(line.Rows)
	m.billed[line.Endpoint] += line.Bytes
	hist(m.request, line.Endpoint).observe(took.Seconds())
	if warehouse > 0 {
		hist(m.warehouse, line.Endpoint).observe(warehouse.Seconds())
	}
}

func hist(m map[string]*histogram, k string) *histogram {
	if m[k] == nil {
		m[k] = &histogram{}
	}
	return m[k]
}

func (m *metrics) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var b strings.Builder
		b.WriteString("# HELP brevis_sql_api_requests_total Requests answered, by endpoint and status.\n")
		b.WriteString("# TYPE brevis_sql_api_requests_total counter\n")
		pairs := make([][2]string, 0, len(m.requests))
		for k := range m.requests {
			pairs = append(pairs, k)
		}
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i][0]+"\x00"+pairs[i][1] < pairs[j][0]+"\x00"+pairs[j][1]
		})
		for _, k := range pairs {
			fmt.Fprintf(&b, "brevis_sql_api_requests_total{endpoint=%q,status=%q} %d\n", k[0], k[1], m.requests[k])
		}
		b.WriteString("# HELP brevis_sql_api_cache_requests_total Requests with a cache, by endpoint and result.\n")
		b.WriteString("# TYPE brevis_sql_api_cache_requests_total counter\n")
		cpairs := make([][2]string, 0, len(m.cache))
		for k := range m.cache {
			cpairs = append(cpairs, k)
		}
		sort.Slice(cpairs, func(i, j int) bool {
			return cpairs[i][0]+"\x00"+cpairs[i][1] < cpairs[j][0]+"\x00"+cpairs[j][1]
		})
		for _, k := range cpairs {
			fmt.Fprintf(&b, "brevis_sql_api_cache_requests_total{endpoint=%q,result=%q} %d\n", k[0], k[1], m.cache[k])
		}
		counter(&b, "rows_total", "Rows returned, by endpoint.", m.rows)
		counter(&b, "bytes_billed_total", "Bytes the warehouse reported scanning, by endpoint.", m.billed)
		histograms(&b, "request_seconds", "Time to answer a request, by endpoint.", m.request)
		histograms(&b, "warehouse_seconds", "Time the warehouse took, by endpoint.", m.warehouse)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(b.String()))
	})
}

func counter(b *strings.Builder, name, help string, m map[string]int64) {
	fmt.Fprintf(b, "# HELP brevis_sql_api_%s %s\n# TYPE brevis_sql_api_%s counter\n", name, help, name)
	for _, k := range sortedKeys(m) {
		fmt.Fprintf(b, "brevis_sql_api_%s{endpoint=%q} %d\n", name, k, m[k])
	}
}

func histograms(b *strings.Builder, name, help string, m map[string]*histogram) {
	fmt.Fprintf(b, "# HELP brevis_sql_api_%s %s\n# TYPE brevis_sql_api_%s histogram\n", name, help, name)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h := m[k]
		for i, le := range buckets {
			fmt.Fprintf(b, "brevis_sql_api_%s_bucket{endpoint=%q,le=%q} %d\n", name, k,
				strconv.FormatFloat(le, 'g', -1, 64), h.counts[i])
		}
		fmt.Fprintf(b, "brevis_sql_api_%s_bucket{endpoint=%q,le=\"+Inf\"} %d\n", name, k, h.n)
		fmt.Fprintf(b, "brevis_sql_api_%s_sum{endpoint=%q} %g\n", name, k, h.sum)
		fmt.Fprintf(b, "brevis_sql_api_%s_count{endpoint=%q} %d\n", name, k, h.n)
	}
}

func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// record is one audit line.
//
// NAMES AND NEVER VALUES. `params` is which parameters were sent; what they
// held is the caller's data, and a log has a retention nobody here chose.
// `args_hash` stands in for the values so two identical requests can be
// told apart from two different ones -- keyed with a secret this process
// drew at start, because a plain hash of a date or a short campaign code is
// a dictionary away from the value it hides.
type record struct {
	At       string   `json:"at"`
	Endpoint string   `json:"endpoint"`
	Tenant   string   `json:"tenant,omitempty"`
	Params   []string `json:"params,omitempty"`
	ArgsHash string   `json:"args_hash,omitempty"`
	Status   int      `json:"status"`
	Rows     int      `json:"rows"`
	Bytes    int64    `json:"bytes,omitempty"`
	Millis   int64    `json:"millis"`
	Cache    string   `json:"cache,omitempty"`
}

// auditKey is this process's secret for args_hash.
var auditKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("no randomness for the audit key: " + err.Error())
	}
	return k
}()

func argsHash(query string, args []dialect.Arg) string {
	mac := hmac.New(sha256.New, auditKey)
	mac.Write([]byte(query))
	for _, a := range args {
		fmt.Fprintf(mac, "\x00%s\x00%T\x00%v", a.Name, a.Value, a.Value)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// finish stamps a line, counts it and writes it.
func (s *Server) finish(line *record, started time.Time, warehouse time.Duration) {
	took := time.Since(started)
	line.At = started.UTC().Format(time.RFC3339)
	line.Millis = took.Milliseconds()
	s.met.observe(line, took, warehouse)
	if s.cfg.Audit == nil {
		return
	}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = s.cfg.Audit.Write(append(b, '\n'))
}

// statusWriter remembers the status a handler wrote.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}
