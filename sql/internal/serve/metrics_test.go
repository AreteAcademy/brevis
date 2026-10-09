package serve

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

func metricsOf(t *testing.T, s *Service) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics answered %d", w.Code)
	}
	return w.Body.String()
}

// ONE SERIES PER OUTCOME, and a query moves exactly one of them.
//
// #65 asked for counters beside the audit line and this shipped without
// them, which the closing comment said out loud: "no SQL text in metrics" was
// true by ABSENCE, and that is not the same as true by design.
func TestAQueryMovesOneCounter(t *testing.T) {
	var audit bytes.Buffer
	p := &priced{estimate: 1 << 20}
	s := queryService(t, p, &audit, 10<<30)

	ask(t, s, queryBody("SELECT 1"))
	ask(t, s, queryBody("DELETE FROM t"))

	body := metricsOf(t, s)
	for _, want := range []string{
		`brevis_sql_queries_total{endpoint="query",outcome="ok"} 1`,
		`brevis_sql_queries_total{endpoint="query",outcome="refused"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition does not hold `%s`:\n%s", want, body)
		}
	}
}

// NO SQL AND NO HASH, EVER. #65's own line is that a WHERE clause carries
// customer data; a metric label is forever and a time series database keeps
// one series per distinct label value. The audit line carries a hash because
// it is a LOG with a retention; a hash here would be a series per distinct
// query, which is both a leak and an outage.
func TestTheExpositionHoldsNoSQLAndNoHash(t *testing.T) {
	const secret = "cpf_12345678900"
	var audit bytes.Buffer
	s := queryService(t, &priced{estimate: 1}, &audit, 10<<30)

	ask(t, s, queryBody("SELECT k FROM t WHERE cpf = '"+secret+"'"))
	ask(t, s, queryBody("DROP TABLE "+secret))

	body := metricsOf(t, s)
	for _, gone := range []string{secret, "SELECT", "DROP", "statement"} {
		if strings.Contains(body, gone) {
			t.Errorf("the exposition holds %q:\n%s", gone, body)
		}
	}
	// A hash is 16 hex characters. Any of them here would be a series per
	// query, which is the shape of an outage as well as a leak.
	if m := regexp.MustCompile(`\b[0-9a-f]{16}\b`).FindString(body); m != "" {
		t.Errorf("the exposition holds what looks like a statement hash: %q", m)
	}
}

// IT IS A VALID EXPOSITION, which is what a scraper needs and what writing
// this by hand risks. Every series has a HELP and a TYPE above it.
func TestTheExpositionIsWellFormed(t *testing.T) {
	s := queryService(t, &priced{estimate: 1}, &bytes.Buffer{}, 10<<30)
	ask(t, s, queryBody("SELECT 1"))

	body := metricsOf(t, s)
	declared := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "# TYPE "):
			declared[strings.Fields(line)[2]] = true
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			name, _, _ := strings.Cut(line, "{")
			name, _, _ = strings.Cut(name, " ")
			base := strings.TrimSuffix(strings.TrimSuffix(name, "_sum"), "_count")
			if !declared[name] && !declared[base] {
				t.Errorf("the series %q has no # TYPE above it", name)
			}
		}
	}
	for _, want := range []string{"# HELP brevis_sql_queries_total", "# TYPE brevis_sql_queries_total counter"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// NOT ON THE PORT THAT READS A WAREHOUSE.
//
// The rule this repository states twice, in the engine and in the gateway: a
// scrape endpoint on the ingress port would either need a session no scraper
// has, or publish every connection name to whoever can reach it. Two
// listeners, and the query one does not route /metrics at all.
func TestMetricsAreNotOnTheQueryListener(t *testing.T) {
	s, err := New(Options{Env: "production", Token: "s3cret", Rows: 10, Bytes: 1 << 20,
		Open: func(context.Context, Table) (dialect.Conn, error) { return &fake{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "brevis_sql_") {
		t.Error("the query listener serves metrics")
	}
}

// THE BYTES ARE COUNTED PER CONNECTION, which is the question an operator
// actually asks: which warehouse is this costing money on.
func TestScannedBytesAreCountedPerConnection(t *testing.T) {
	s := queryService(t, &priced{estimate: 5 << 20}, &bytes.Buffer{}, 10<<30)
	ask(t, s, queryBody("SELECT 1"))

	if want := `brevis_sql_scanned_bytes_total{connection="acme-prod"} 5242880`; !strings.Contains(metricsOf(t, s), want) {
		t.Errorf("the exposition does not hold `%s`:\n%s", want, metricsOf(t, s))
	}
}

// A PREVIEW IS A QUERY THE WAREHOUSE BILLS FOR, and it was never audited.
//
// #65's criterion is "every query produces exactly one audit line", and this
// shipped answering it with `/v1/query` alone. A preview reads a customer's
// table, costs the same bytes and could be refused for the same reasons --
// and left no trace at all. Found while wiring the counters, because the
// counter for `endpoint="preview"` could never move.
func TestAPreviewIsAuditedAndCountedToo(t *testing.T) {
	var audit bytes.Buffer
	f := &fake{}
	s, err := New(Options{Env: EnvLocal, Rows: 100, Bytes: 1 << 30, Audit: &audit,
		Open: func(context.Context, Table) (dialect.Conn, error) { return f, nil }})
	if err != nil {
		t.Fatal(err)
	}
	post(t, s, `{"target":"bigquery://acme-prod/bronze/orders"}`)

	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"event":"preview"`) {
		t.Fatalf("a preview left %d audit line(s):\n%s", len(lines), audit.String())
	}
	for _, want := range []string{`"outcome":"ok"`, `"connection":"acme-prod"`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the preview's audit line does not hold %s: %s", want, lines[0])
		}
	}
	if strings.Contains(audit.String(), "SELECT") {
		t.Errorf("the preview's audit line holds SQL: %s", audit.String())
	}
	if want := `brevis_sql_queries_total{endpoint="preview",outcome="ok"} 1`; !strings.Contains(metricsOf(t, s), want) {
		t.Errorf("no counter moved for a preview:\n%s", metricsOf(t, s))
	}
}
