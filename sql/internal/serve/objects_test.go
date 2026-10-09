package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// lister is a warehouse that can say what it holds, and counts being asked.
type lister struct {
	priced
	rels  []dialect.Relation
	asked int
	mu    sync.Mutex
}

func (l *lister) Relations(context.Context) ([]dialect.Relation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked++
	return l.rels, nil
}

func objects(t *testing.T, s *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/objects", strings.NewReader(body))
	if s.opt.Token != "" {
		r.Header.Set("Authorization", "Bearer "+s.opt.Token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func listed(t *testing.T, l dialect.Conn, audit *bytes.Buffer) *Service {
	t.Helper()
	s, err := New(Options{Env: EnvLocal, Rows: 100, Bytes: 10 << 30, Audit: audit,
		Open: func(context.Context, Table) (dialect.Conn, error) { return l, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// WHAT A SELECT COULD NAME, for one connection.
func TestObjectsListsWhatTheWarehouseHolds(t *testing.T) {
	l := &lister{rels: []dialect.Relation{{Schema: "bronze", Name: "orders"}, {Schema: "gold", Name: "daily"}}}
	w := objects(t, listed(t, l, &bytes.Buffer{}), `{"target":"bigquery://acme-prod/bronze/orders"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	var got objectsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Relations) != 2 || got.Relations[0].Schema != "bronze" {
		t.Errorf("listed %+v", got.Relations)
	}
}

// ONCE PER CONNECTION, and that is the whole shape of this endpoint.
//
// Measured against BigQuery: every INFORMATION_SCHEMA query is billed at a
// 10 MiB floor whatever comes back, so a tree asking per node spends 10 MB a
// click. The answer is held; the cost is paid once.
func TestTheWarehouseIsAskedOncePerConnection(t *testing.T) {
	l := &lister{rels: []dialect.Relation{{Schema: "bronze", Name: "orders"}}}
	s := listed(t, l, &bytes.Buffer{})
	for range 4 {
		objects(t, s, `{"target":"bigquery://acme-prod/bronze/orders"}`)
	}
	if l.asked != 1 {
		t.Errorf("the warehouse was listed %d times, and a listing costs money", l.asked)
	}
}

// A WAREHOUSE THAT CANNOT SAY IS NOT A FAILURE, it is an answer: the tree
// draws nothing rather than an error, and a dialect without the capability
// is not refused for a question it was never asked.
func TestAWarehouseThatCannotListSaysSo(t *testing.T) {
	var audit bytes.Buffer
	w := objects(t, listed(t, &priced{estimate: 1}, &audit),
		`{"target":"bigquery://acme-prod/bronze/orders"}`)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("answered %d, wanted a 501", w.Code)
	}
	// AND THE AUDIT LINE SAYS THE SAME THING. A status code and an outcome
	// that disagree make the log useless for the question it is read for,
	// and a mutation writing `ok` here passed on the code alone.
	if !strings.Contains(audit.String(), `"outcome":"unsupported"`) {
		t.Errorf("the audit line does not say what happened:\n%s", audit.String())
	}
}

// ONE AUDIT LINE PER LISTING, with no name in it.
//
// "Who looked at what" is the question an audit of a data tool is read for,
// and a listing left no trace at all until this. It carries the connection
// and how many came back -- never a schema or a table, which is the same
// rule the query line follows about SQL.
func TestAListingIsAudited(t *testing.T) {
	var audit bytes.Buffer
	l := &lister{rels: []dialect.Relation{{Schema: "secret_schema", Name: "secret_table"}}}
	objects(t, listed(t, l, &audit), `{"target":"bigquery://acme-prod/bronze/orders"}`)

	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d audit lines:\n%s", len(lines), audit.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatal(err)
	}
	if line["event"] != "objects" {
		t.Errorf("the line says event %v", line["event"])
	}
	if line["connection"] != "acme-prod" {
		t.Errorf("it does not name the connection: %s", lines[0])
	}
	for _, leak := range []string{"secret_schema", "secret_table"} {
		if strings.Contains(audit.String(), leak) {
			t.Errorf("the audit line holds %q", leak)
		}
	}
}

// A BAD TARGET IS REFUSED BEFORE A CONNECTION EXISTS, the rule both other
// endpoints already follow.
func TestObjectsRefusesABadTargetBeforeConnecting(t *testing.T) {
	opened := false
	s, err := New(Options{Env: EnvLocal, Rows: 10, Bytes: 1 << 20,
		Open: func(context.Context, Table) (dialect.Conn, error) {
			opened = true
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if w := objects(t, s, `{"target":"redshift://c/public/orders"}`); w.Code != http.StatusBadRequest {
		t.Errorf("answered %d", w.Code)
	}
	if opened {
		t.Error("it opened a connection for a target it had already refused")
	}
}

// THE WIRE IS LOWERCASE, like every other response this service writes.
//
// This endpoint marshalled `dialect.Relation` -- a DOMAIN type with no tags
// -- straight onto the wire, so it answered `{"Schema":…,"Name":…}` while
// `/v1/query` beside it answers `{"columns":…,"rows":…}`. Nothing here saw
// it: the test above unmarshals into the same Go struct it marshalled from,
// which round-trips whatever the keys are, and the console's decoder is
// case-insensitive, so the one consumer worked by luck.
//
// It was found by curling the running service. The fix is the rule the other
// endpoints already follow -- a wire type with explicit tags, built from the
// domain type -- and this test reads the BYTES, because that is the only
// place the difference exists.
func TestAListingIsWrittenInThisServicesOwnCasing(t *testing.T) {
	l := &lister{rels: []dialect.Relation{{Schema: "bronze", Name: "orders"}}}
	body := objects(t, listed(t, l, &bytes.Buffer{}), `{"target":"bigquery://acme-prod/bronze/orders"}`).Body.String()

	for _, want := range []string{`"schema":"bronze"`, `"name":"orders"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the listing does not carry %s: %s", want, body)
		}
	}
	for _, gone := range []string{`"Schema"`, `"Name"`} {
		if strings.Contains(body, gone) {
			t.Errorf("the listing leaks the domain type's %s: %s", gone, body)
		}
	}
}
