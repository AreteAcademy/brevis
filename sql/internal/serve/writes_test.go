package serve

import (
	"bytes"
	"context"
	"net/http"
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
