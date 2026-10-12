package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

func observed(t *testing.T, audit *bytes.Buffer) *Server {
	t.Helper()
	w := &warehouse{cols: []string{"k"}, rows: [][]any{{1}, {2}}}
	e := &Endpoint{Name: "clicks", Path: "/v1/clicks",
		Params: map[string]Param{"campaign": {Type: "string", MaxLen: 32}},
		SQL:    "select k from t where w = {{ tenant }} {% if campaign %}and c = {{ campaign }}{% endif %}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Name: "x", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 10,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Endpoints: []*Endpoint{e}}
	// A nil *bytes.Buffer in an io.Writer is not a nil writer.
	if audit != nil {
		cfg.Audit = audit
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func scrape(t *testing.T, s *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// EVERY SERIES HAS ITS HELP AND ITS TYPE, and counts what happened. Written
// by hand, as `serve` writes its own, because the client library is 51
// packages and 8 MB over both ceilings for five series.
func TestTheSeriesCountWhatHappened(t *testing.T) {
	s := observed(t, nil)
	get(s, "/v1/clicks?campaign=c")
	get(s, "/v1/clicks?campaign=c")
	get(s, "/v1/clicks?nope=1")
	body := scrape(t, s)

	for _, want := range []string{
		`brevis_sql_api_requests_total{endpoint="clicks",status="200"} 2`,
		`brevis_sql_api_requests_total{endpoint="clicks",status="400"} 1`,
		`brevis_sql_api_rows_total{endpoint="clicks"} 4`,
		`brevis_sql_api_request_seconds_count{endpoint="clicks"} 3`,
		`brevis_sql_api_warehouse_seconds_count{endpoint="clicks"} 2`,
		`brevis_sql_api_request_seconds_bucket{endpoint="clicks",le="+Inf"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in\n%s", want, body)
		}
	}
	for _, series := range []string{"requests_total", "rows_total", "bytes_billed_total", "request_seconds", "warehouse_seconds"} {
		for _, kind := range []string{"# HELP brevis_sql_api_" + series + " ", "# TYPE brevis_sql_api_" + series + " "} {
			if !strings.Contains(body, kind) {
				t.Errorf("no %q", kind)
			}
		}
	}
}

// NO TENANT IN A LABEL, AND NO VALUE ANYWHERE. A label is a series forever,
// one per tenant is a cardinality problem and a customer list on a port;
// a value is the caller's data.
func TestMetricsHoldNoTenantAndNoValue(t *testing.T) {
	s := observed(t, nil)
	get(s, "/v1/clicks?campaign=secret-campaign")
	body := scrape(t, s)
	if strings.Contains(body, "tenant") || strings.Contains(body, LocalTenant+`"`) || strings.Contains(body, "secret-campaign") {
		t.Errorf("the exposition holds a tenant or a value:\n%s", body)
	}
}

// A REQUEST WITH NO VALID KEY IS COUNTED, under no endpoint: a 401 storm is
// worth seeing, and the path it asked for is the caller's to choose -- a
// label from it would be a series per guess.
func TestAnUnauthorisedRequestIsCountedUnderNoEndpoint(t *testing.T) {
	h, err := guarded(t, &warehouse{}, hashOf("a-key-long-enough-0123456789abcdef")+"=acme")
	if err != nil {
		t.Fatal(err)
	}
	withKey(h, "/v1/guess-1", "")
	withKey(h, "/v1/guess-2", "")
	if body := scrape(t, h); !strings.Contains(body, `brevis_sql_api_requests_total{endpoint="-",status="401"} 2`) ||
		strings.Contains(body, "guess") {
		t.Errorf("exposition:\n%s", body)
	}
}

// ONE AUDIT LINE PER REQUEST: who, which endpoint, which parameters by
// NAME, a hash standing for the arguments, the outcome. Never a value.
func TestEachRequestWritesOneAuditLineWithNamesAndNoValues(t *testing.T) {
	var audit bytes.Buffer
	s := observed(t, &audit)
	get(s, "/v1/clicks?campaign=secret-campaign")

	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d lines: %s", len(lines), audit.String())
	}
	if strings.Contains(lines[0], "secret-campaign") {
		t.Errorf("the audit line holds a value: %s", lines[0])
	}
	var line struct {
		Endpoint string   `json:"endpoint"`
		Tenant   string   `json:"tenant"`
		Params   []string `json:"params"`
		ArgsHash string   `json:"args_hash"`
		Status   int      `json:"status"`
		Rows     int      `json:"rows"`
		At       string   `json:"at"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatal(err)
	}
	if line.Endpoint != "clicks" || line.Tenant != LocalTenant || len(line.Params) != 1 ||
		line.Params[0] != "campaign" || len(line.ArgsHash) != 64 || line.Status != 200 || line.Rows != 2 || line.At == "" {
		t.Errorf("audit line %+v", line)
	}
}
