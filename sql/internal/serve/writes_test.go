package serve

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// writesTo builds a service that may write on the named connections.
func writesTo(t *testing.T, c dialect.Conn, audit *bytes.Buffer, on ...string) *Service {
	t.Helper()
	s, err := New(Options{
		Env: "production", Token: "s3cret", Rows: 100, Bytes: 10 << 30,
		Audit: audit, Writable: on,
		Open: func(context.Context, Table) (dialect.Conn, error) { return c, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A WRITE RUNS WHERE SOMEBODY NAMED THE CONNECTION, and nowhere else.
//
// The statement here is refused by every layer this service has: the
// tokeniser reads `CREATE TABLE` and stops, and the warehouse calls it
// CREATE_TABLE. Both have to stand down together, which is why this asserts
// one 200 rather than three internals -- a feature that unlocked two of three
// layers would answer 400 and look like it worked from the outside.
func TestAWriteRunsOnAConnectionThatNamesIt(t *testing.T) {
	var audit bytes.Buffer
	p := &planned{writable: writable{priced: priced{estimate: 1}, can: true},
		kind: "CREATE_TABLE", bytes: 1}
	res := askAs(t, writesTo(t, p, &audit, "acme-prod"), "/v1/query",
		queryBody("CREATE TABLE t (n INT64)"))

	if res.Code != http.StatusOK {
		t.Fatalf("a write on a connection that allows writes answered %d: %s", res.Code, res.Body.String())
	}
	if p.ran == 0 {
		t.Error("it answered 200 and never ran the statement")
	}
	// AND THE LINE SAYS SO. An audit that cannot tell a write from a query
	// is an audit nobody can answer "what changed" with, which is the only
	// question this mode makes somebody ask.
	if !strings.Contains(audit.String(), `"wrote":true`) {
		t.Errorf("the audit line does not mark the write:\n%s", audit.String())
	}
}

// AND THE SAME STATEMENT IS REFUSED ONE CONNECTION OVER.
//
// This is the reason the variable names connections instead of being a
// switch: one service reaches several warehouses, and unlocking the one
// somebody is developing against must not unlock the one beside it.
func TestTheSameWriteIsRefusedOnAConnectionThatDoesNot(t *testing.T) {
	var audit bytes.Buffer
	p := &planned{writable: writable{priced: priced{estimate: 1}, can: true},
		kind: "CREATE_TABLE", bytes: 1}
	res := askAs(t, writesTo(t, p, &audit, "somewhere-else"), "/v1/query",
		queryBody("CREATE TABLE t (n INT64)"))

	if res.Code != http.StatusBadRequest {
		t.Fatalf("a write on a connection nobody unlocked answered %d", res.Code)
	}
	if p.ran != 0 {
		t.Error("it ran on a connection nobody unlocked")
	}
}

// A SELECT ON AN UNLOCKED CONNECTION IS STILL A SELECT.
//
// The marker has to mean "this changed something". Marking every line from an
// unlocked connection would be cheaper and would make the field useless on
// the day it matters, which is the day somebody greps for it.
func TestASelectOnAWritableConnectionIsNotMarkedAsAWrite(t *testing.T) {
	var audit bytes.Buffer
	p := &planned{writable: writable{priced: priced{estimate: 1}},
		kind: "SELECT", reads: true, bytes: 1}
	res := askAs(t, writesTo(t, p, &audit, "acme-prod"), "/v1/query", queryBody("SELECT 1"))

	if res.Code != http.StatusOK {
		t.Fatalf("a query on an unlocked connection answered %d", res.Code)
	}
	if strings.Contains(audit.String(), `"wrote":true`) {
		t.Errorf("a SELECT was recorded as a write:\n%s", audit.String())
	}
}

// THE GUARD IS NOT OPENED ON A CONNECTION THAT MAY WRITE.
//
// Postgres refuses a write inside it, so leaving it on would make the unlock
// a 500 from the server rather than a feature -- and the failure would read
// as the warehouse's, not as ours.
func TestAWritableConnectionIsNotWrappedInTheGuard(t *testing.T) {
	var audit bytes.Buffer
	g := &guarded{writable: writable{priced: priced{estimate: 1}, can: true}}
	res := askAs(t, writesTo(t, g, &audit, "acme-prod"), "/v1/query",
		queryBody("DELETE FROM t WHERE TRUE"))

	if res.Code != http.StatusOK {
		t.Fatalf("a write on an unlocked Postgres-shaped connection answered %d: %s",
			res.Code, res.Body.String())
	}
	if g.wrapped != 0 {
		t.Errorf("the read-only guard was opened %d time(s) on a connection that may write", g.wrapped)
	}
}

// AND THE MONEY STILL BOUNDS IT.
//
// The price comes out of the same dry run as the verdict, so an
// implementation that skipped the plan to skip the verdict would lose the
// cost ceiling and the budget with it -- silently, because a write that runs
// is what every other test here is looking for.
func TestAWriteIsStillBoundedByTheCostCeiling(t *testing.T) {
	var audit bytes.Buffer
	p := &planned{writable: writable{priced: priced{estimate: 1}},
		kind: "CREATE_TABLE", bytes: 1 << 20}
	s, err := New(Options{
		Env: "production", Token: "s3cret", Rows: 100, Bytes: 1024,
		Audit: &audit, Writable: []string{"acme-prod"},
		Open: func(context.Context, Table) (dialect.Conn, error) { return p, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	res := askAs(t, s, "/v1/query", queryBody("CREATE TABLE t AS SELECT * FROM huge"))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("a write over the cost ceiling answered %d", res.Code)
	}
	if p.ran != 0 {
		t.Error("it ran over the ceiling")
	}
}

// WRITES CANNOT BE UNLOCKED ON A PORT WITH NO TOKEN, AND `local` IS NOT AN
// EXCEPTION.
//
// `BREVIS_ENV=local` starts with no token today and says so on the banner:
// anybody who reaches the port can READ the warehouse. A port that can also
// DROP TABLE is a different object, and the line BREVIS_ENV draws for reading
// is not the line to draw for writing. It costs the operator one variable
// they already know how to set.
func TestWritesCannotBeUnlockedWithoutAToken(t *testing.T) {
	for _, env := range []string{"production", EnvLocal} {
		_, err := New(Options{
			Env: env, Rows: 100, Bytes: 1 << 20, Writable: []string{"acme-prod"},
			Open: func(context.Context, Table) (dialect.Conn, error) { return &fake{}, nil },
		})
		if err == nil {
			t.Fatalf("BREVIS_ENV=%s: a service that may write started with no token", env)
		}
		if !strings.Contains(err.Error(), "BREVIS_SQL_SERVE_WRITES") {
			t.Errorf("BREVIS_ENV=%s: the refusal does not name the variable: %v", env, err)
		}
	}
}

// A WAREHOUSE WITH NO VERDICT STILL GETS ITS WRITES MARKED.
//
// FOUND BY RUNNING IT. Every test above drives a fake that implements
// Planner, and `line.Wrote` was set only in that branch -- so on Postgres,
// which offers a read-only guard and no verdict, a CREATE TABLE and an INSERT
// went through and the audit called them queries. Measured against the real
// service: three statements, three lines, `wrote` false on all of them.
//
// The tokeniser is what labels where the warehouse says nothing. It is the
// same classifier that would have REFUSED the statement had the connection
// been locked, used here only to describe it -- and it carries its own known
// hole: `SELECT nextval('s')` is a write it reads as a query, so on a
// guardless warehouse that one line would say query. It is better than a
// field that is always false, and worse than a verdict; both halves are true
// and the warehouse's answer wins wherever there is one.
func TestAWriteIsMarkedOnAWarehouseThatCannotClassify(t *testing.T) {
	var audit bytes.Buffer
	g := &guarded{writable: writable{priced: priced{estimate: 1}, can: true}}
	if _, plans := any(g).(dialect.Planner); plans {
		t.Fatal("this fake classifies, so it cannot test the warehouse that does not")
	}
	res := askAs(t, writesTo(t, g, &audit, "acme-prod"), "/v1/query",
		queryBody("CREATE TABLE t (n int)"))

	if res.Code != http.StatusOK {
		t.Fatalf("a write on an unlocked guardless connection answered %d", res.Code)
	}
	if !strings.Contains(audit.String(), `"wrote":true`) {
		t.Errorf("the write is not marked:\n%s", audit.String())
	}
}

// AND A QUERY THERE IS STILL A QUERY.
func TestASelectIsNotMarkedOnAWarehouseThatCannotClassify(t *testing.T) {
	var audit bytes.Buffer
	g := &guarded{writable: writable{priced: priced{estimate: 1}, can: true}}
	res := askAs(t, writesTo(t, g, &audit, "acme-prod"), "/v1/query", queryBody("SELECT 1"))

	if res.Code != http.StatusOK {
		t.Fatalf("a query on an unlocked connection answered %d", res.Code)
	}
	if strings.Contains(audit.String(), `"wrote":true`) {
		t.Errorf("a SELECT was recorded as a write:\n%s", audit.String())
	}
}

// writing is a warehouse that runs a write and says how much it changed.
type writing struct {
	planned
	changed int64
	counted bool
	wrote   []string
}

func (w *writing) Write(_ context.Context, statement string) (dialect.Result, error) {
	w.wrote = append(w.wrote, statement)
	return dialect.Result{Columns: []string{}, Rows: [][]any{},
		Changed: w.changed, Counted: w.counted}, nil
}

// A WRITE ANSWERS WITH WHAT IT CHANGED, NOT WITH AN EMPTY GRID.
//
// "Did my UPDATE hit zero rows or ten thousand" is the only question anybody
// asks afterwards, and a table with no columns answers neither.
func TestAWriteAnswersWithTheCountTheWarehouseGave(t *testing.T) {
	var audit bytes.Buffer
	w := &writing{planned: planned{writable: writable{priced: priced{estimate: 1}},
		kind: "UPDATE", bytes: 1}, changed: 12, counted: true}
	res := askAs(t, writesTo(t, w, &audit, "acme-prod"), "/v1/query",
		queryBody("UPDATE t SET n = 1"))

	if res.Code != http.StatusOK {
		t.Fatalf("a write answered %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{`"wrote":true`, `"changed":12`} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer has no %s: %s", want, body)
		}
	}
	// AND IT WAS RUN AS A WRITE. The reader is still there and would have
	// returned an empty grid without complaint, which is the shape this
	// replaces.
	if len(w.wrote) != 1 {
		t.Errorf("the statement went somewhere other than Write: %v", w.wrote)
	}
	if w.ran != 0 {
		t.Error("a write was sent to the reader")
	}
	if !strings.Contains(audit.String(), `"changed":12`) {
		t.Errorf("the audit line does not carry the count:\n%s", audit.String())
	}
}

// AND A STATEMENT THAT CHANGES NO ROWS SAYS NOTHING ABOUT ROWS.
//
// `CREATE TABLE` and `UPDATE ... WHERE false` both changed nothing and are
// not the same answer. Printing "0 rows" for the first reads as a failure to
// whoever ran it, so the key is absent rather than zero.
func TestADDLAnswersWithoutARowCount(t *testing.T) {
	var audit bytes.Buffer
	w := &writing{planned: planned{writable: writable{priced: priced{estimate: 1}},
		kind: "CREATE_TABLE", bytes: 1}, counted: false}
	res := askAs(t, writesTo(t, w, &audit, "acme-prod"), "/v1/query",
		queryBody("CREATE TABLE t (n int)"))

	if res.Code != http.StatusOK {
		t.Fatalf("a DDL answered %d", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, `"wrote":true`) {
		t.Errorf("the answer does not say it wrote: %s", body)
	}
	if strings.Contains(body, `"changed"`) {
		t.Errorf("a DDL claimed a row count: %s", body)
	}
}

// AND ZERO IS A COUNT, which is the half an `omitempty` would quietly lose.
func TestAWriteThatMatchedNothingStillSaysZero(t *testing.T) {
	var audit bytes.Buffer
	w := &writing{planned: planned{writable: writable{priced: priced{estimate: 1}},
		kind: "UPDATE", bytes: 1}, changed: 0, counted: true}
	res := askAs(t, writesTo(t, w, &audit, "acme-prod"), "/v1/query",
		queryBody("UPDATE t SET n = 1 WHERE FALSE"))

	if res.Code != http.StatusOK {
		t.Fatalf("answered %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"changed":0`) {
		t.Errorf("an update that matched nothing did not say zero: %s", res.Body.String())
	}
}

// A WAREHOUSE WITH NO Writer STILL RUNS THE STATEMENT.
//
// The interface is optional, and a dialect that has not got it must not lose
// the feature -- it loses the COUNT. Measured on real Postgres before Writer
// existed: a DDL and a DML travel the read path, change the data, and come
// back with no columns and no rows.
func TestAWarehouseWithNoWriterStillRunsTheWrite(t *testing.T) {
	var audit bytes.Buffer
	p := &planned{writable: writable{priced: priced{estimate: 1}}, kind: "INSERT", bytes: 1}
	if _, w := any(p).(dialect.Writer); w {
		t.Fatal("this fake writes, so it cannot test the warehouse that does not")
	}
	res := askAs(t, writesTo(t, p, &audit, "acme-prod"), "/v1/query",
		queryBody("INSERT INTO t (n) VALUES (1)"))

	if res.Code != http.StatusOK {
		t.Fatalf("answered %d", res.Code)
	}
	if p.ran == 0 {
		t.Error("the statement never ran")
	}
	if strings.Contains(res.Body.String(), `"changed"`) {
		t.Errorf("a warehouse that counts nothing reported a count: %s", res.Body.String())
	}
}

// THE SERVICE SAYS WHICH CONNECTIONS MAY BE WRITTEN TO, ON AN ANSWER THE
// CONSOLE ALREADY ASKS FOR.
//
// Not a new endpoint: the tree fetches `/v1/objects` for the connection
// somebody is looking at, which is exactly the scope the answer belongs to.
// A service-wide flag would be wrong -- one service reaches several
// warehouses and unlocks some of them.
func TestObjectsSaysWhetherThisConnectionMayBeWrittenTo(t *testing.T) {
	var audit bytes.Buffer
	l := &lister{}
	body := askAs(t, writesTo(t, l, &audit, "acme-prod"), "/v1/objects",
		`{"target":"bigquery://acme-prod/bronze/orders"}`).Body.String()
	if !strings.Contains(body, `"writable":true`) {
		t.Errorf("the listing does not say the connection may be written to: %s", body)
	}

	body = askAs(t, writesTo(t, l, &audit, "somewhere-else"), "/v1/objects",
		`{"target":"bigquery://acme-prod/bronze/orders"}`).Body.String()
	if strings.Contains(body, `"writable":true`) {
		t.Errorf("a locked connection was listed as writable: %s", body)
	}
}

// AND THE EXPOSITION CARRIES IT, so the mode is visible on a dashboard and
// not only in a log somebody did not keep. A gauge and not a counter: it is
// a state, and it is the state an alert would be written against.
func TestTheMetricsExposeWhichConnectionsMayWrite(t *testing.T) {
	var audit bytes.Buffer
	s := writesTo(t, &lister{}, &audit, "acme-prod", "scratch")
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	got := rec.Body.String()
	for _, want := range []string{
		`brevis_sql_writable_connection{connection="acme-prod"} 1`,
		`brevis_sql_writable_connection{connection="scratch"} 1`,
		"# TYPE brevis_sql_writable_connection gauge",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the exposition has no %q:\n%s", want, got)
		}
	}
}

// AND A SERVICE THAT UNLOCKS NOTHING PUBLISHES NOTHING, so the series only
// exists where there is something to see.
func TestTheMetricsAreSilentWhenNothingMayWrite(t *testing.T) {
	var audit bytes.Buffer
	s := writesTo(t, &lister{}, &audit)
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(rec.Body.String(), "brevis_sql_writable_connection") {
		t.Errorf("a read-only service published a writable gauge:\n%s", rec.Body.String())
	}
}
