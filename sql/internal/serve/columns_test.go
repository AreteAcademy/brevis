package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// describer is a warehouse that can say what one relation holds.
type describer struct {
	priced
	cols  []dialect.Column
	asked []dialect.Relation
	mu    sync.Mutex
}

func (d *describer) Columns(_ context.Context, r dialect.Relation) ([]dialect.Column, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, r)
	return d.cols, nil
}

func columns(t *testing.T, s *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/columns", strings.NewReader(body))
	if s.opt.Token != "" {
		r.Header.Set("Authorization", "Bearer "+s.opt.Token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

const oneRelation = `{"target":"bigquery://acme-prod/bronze/orders","schema":"bronze","name":"orders"}`

// NAMES AND TYPES, IN THE WAREHOUSE'S OWN ORDER.
//
// CHECKPOINT D put columns at this level deliberately: "columns stay lazy,
// per relation, because that is the level a preview already discloses and
// the one nobody expands by accident".
func TestColumnsSaysWhatARelationHolds(t *testing.T) {
	d := &describer{cols: []dialect.Column{{Name: "id", Type: "INT64"}, {Name: "at", Type: "TIMESTAMP"}}}
	w := columns(t, listed(t, d, &bytes.Buffer{}), oneRelation)

	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	// THE BYTES, not a round trip through the same struct: that is how the
	// listing shipped `{"Schema":…}` for a day.
	for _, want := range []string{`"name":"id"`, `"type":"INT64"`, `"name":"at"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the answer does not carry %s: %s", want, w.Body)
		}
	}
	if len(d.asked) != 1 || d.asked[0] != (dialect.Relation{Schema: "bronze", Name: "orders"}) {
		t.Errorf("the warehouse was asked about %+v", d.asked)
	}
}

// ONE RELATION, ASKED ONCE. The same bargain the listing struck: a tree that
// re-asked on every redraw would spend BigQuery's 10 MiB floor per click.
func TestARelationIsDescribedOncePerSession(t *testing.T) {
	d := &describer{cols: []dialect.Column{{Name: "id", Type: "INT64"}}}
	s := listed(t, d, &bytes.Buffer{})
	for range 4 {
		columns(t, s, oneRelation)
	}
	if len(d.asked) != 1 {
		t.Errorf("the warehouse was asked %d times", len(d.asked))
	}
	// AND THE CACHE IS PER RELATION, not per connection: a second relation
	// is a second question, and a cache keyed on the connection alone would
	// answer it with the first one's columns.
	columns(t, s, `{"target":"bigquery://acme-prod/bronze/orders","schema":"gold","name":"daily"}`)
	if len(d.asked) != 2 {
		t.Errorf("a different relation was answered from the first one's cache: %+v", d.asked)
	}
}

// A WAREHOUSE THAT CANNOT DESCRIBE SAYS SO, and the screen keeps working.
func TestAWarehouseThatCannotDescribeSaysSo(t *testing.T) {
	w := columns(t, listed(t, &priced{}, &bytes.Buffer{}), oneRelation)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
}

// THE AUDIT SAYS SOMEBODY LOOKED, AND NEVER AT WHAT.
//
// CHECKPOINT D: a listing "carries the connection and the level, never a
// name -- the same rule the query line follows about SQL". A relation name
// is the one new thing this endpoint knows, and it is exactly what must not
// be written down.
func TestDescribingIsAudited(t *testing.T) {
	var audit bytes.Buffer
	d := &describer{cols: []dialect.Column{{Name: "secret_column", Type: "STRING"}}}
	columns(t, listed(t, d, &audit), oneRelation)

	var line record
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &line); err != nil {
		t.Fatalf("%v: %s", err, audit.String())
	}
	if line.Event != "columns" {
		t.Errorf("the line calls it %q", line.Event)
	}
	if line.Outcome != "ok" {
		t.Errorf("outcome %q", line.Outcome)
	}
	if line.Connection == "" {
		t.Error("the line does not say which connection was browsed")
	}
	for _, leak := range []string{"orders", "bronze", "secret_column"} {
		if strings.Contains(audit.String(), leak) {
			t.Errorf("the audit line carries %q: %s", leak, audit.String())
		}
	}
}

// A RELATION IS REQUIRED, and asked for by its parts.
//
// The target names the CONNECTION, as it does for a listing; the relation is
// its own two fields rather than the target's tail, because the tree's nodes
// come from a listing and a listing answers in those two fields.
func TestColumnsRefusesARequestWithNoRelation(t *testing.T) {
	d := &describer{}
	for _, body := range []string{
		`{"target":"bigquery://acme-prod/bronze/orders"}`,
		`{"target":"bigquery://acme-prod/bronze/orders","schema":"bronze"}`,
		`{"target":"bigquery://acme-prod/bronze/orders","name":"orders"}`,
	} {
		if w := columns(t, listed(t, d, &bytes.Buffer{}), body); w.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d", body, w.Code)
		}
	}
	if len(d.asked) != 0 {
		t.Errorf("a request with no relation reached the warehouse: %+v", d.asked)
	}
}

// A WIDE RELATION IS CUT, AND SAYS SO. The same sentence the listing makes:
// a browser silently showing half the columns is how somebody concludes a
// column does not exist.
func TestAWideRelationIsCutAndSaysSo(t *testing.T) {
	var many []dialect.Column
	for i := range columnCeiling + 10 {
		many = append(many, dialect.Column{Name: fmt.Sprintf("c%d", i), Type: "STRING"})
	}
	w := columns(t, listed(t, &describer{cols: many}, &bytes.Buffer{}), oneRelation)

	var got columnsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) != columnCeiling {
		t.Errorf("%d columns crossed the wire", len(got.Columns))
	}
	if !got.Truncated {
		t.Error("the answer was cut and does not say so")
	}
}

// AND IT REFUSES A TARGET BEFORE IT CONNECTS, like every other endpoint.
func TestColumnsRefusesABadTargetBeforeConnecting(t *testing.T) {
	d := &describer{}
	if w := columns(t, listed(t, d, &bytes.Buffer{}), `{"target":"not a target","schema":"s","name":"n"}`); w.Code != http.StatusBadRequest {
		t.Errorf("answered %d", w.Code)
	}
	if len(d.asked) != 0 {
		t.Error("a bad target reached the warehouse")
	}
}

// EVERY ENDPOINT IS COUNTED, and the list of which ones is a place to forget.
//
// `audit` feeds the metrics from a SWITCH over the event name. A new
// endpoint that is not named there is audited and invisible: the counters
// would say the service answered nothing while somebody browsed a warehouse
// all afternoon. CHECKPOINT D asked for exactly this -- "it costs bytes, so
// it meets the byte ceiling and the counters".
func TestDescribingIsCounted(t *testing.T) {
	s := listed(t, &describer{cols: []dialect.Column{{Name: "id", Type: "INT64"}}}, &bytes.Buffer{})
	columns(t, s, oneRelation)

	// `Metrics()` AND NOT `Handler()`: the exposition is a separate handler
	// bound to its own address, and `Handler` deliberately does not route
	// /metrics at all. A test here that scraped the request port would pass
	// only if that rule were broken.
	w := httptest.NewRecorder()
	s.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		`brevis_sql_queries_total{endpoint="columns",outcome="ok"} 1`,
		`brevis_sql_rows_returned_total{endpoint="columns"} 1`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the counters do not carry `%s`", want)
		}
	}
}
