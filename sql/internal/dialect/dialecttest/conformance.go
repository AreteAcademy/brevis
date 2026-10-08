// Package dialecttest is the suite every dialect has to pass.
//
// IT EXISTS BEFORE THE SECOND DIALECT DOES, and that is the reason it exists
// at all. An interface with one implementation is a description of that
// implementation; the second one added later agrees with it wherever the
// author happened to look and differs everywhere else, and the difference is
// found by whoever pointed a project at the other warehouse. Here it is
// found by `go test`.
//
// It runs against a REAL warehouse and nothing else. Every claim it makes is
// about what the server did -- a dialect that returns the right strings and
// builds the wrong thing passes a test of its strings and fails this one.
package dialecttest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

// Run is the whole suite. A dialect's own test calls it with a live DSN.
func Run(t *testing.T, d dialect.Dialect, dsn string) {
	t.Helper()

	ctx := context.Background()
	conn, err := d.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("%s: connecting: %v", d.Name(), err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	// A schema per run, so two runs never meet and a failure leaves
	// something to look at without blocking the next one.
	schema := fmt.Sprintf("bvs_conf_%d", time.Now().UnixNano())
	for _, s := range d.EnsureSchema(schema) {
		if err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("%s: making the schema: %v", d.Name(), err)
		}
	}
	t.Cleanup(func() { dropSchema(d, conn, schema) })

	h := &harness{t: t, d: d, conn: conn, schema: schema, ctx: ctx}

	t.Run("a schema that was not there is made", h.schemaIsMade)
	t.Run("a view is built and can be read", h.viewIsBuilt)
	t.Run("building the same view again is fine", h.viewIsIdempotent)
	t.Run("a table is built and can be read", h.tableIsBuilt)
	t.Run("a table holds its rows and a view does not", h.tableHoldsRowsAViewDoesNot)
	t.Run("a view replaces a table of that name", h.viewReplacesATable)
	t.Run("a table replaces a view of that name", h.tableReplacesAView)
	t.Run("nothing of that name is Absent", h.absentIsAbsent)
	t.Run("a string literal survives what is in it", h.literalsSurvive)
	t.Run("a relation has a catalog target", h.targetNamesTheRelation)
}

type harness struct {
	t      *testing.T
	d      dialect.Dialect
	conn   dialect.Conn
	schema string
	ctx    context.Context
}

// build runs one model the way the build loop does: ask what is there, then
// run what the dialect says. Going through kindOf rather than passing a Kind
// is deliberate -- it means every test here also exercises KindOf, which is
// the one method a dialect can get wrong without any statement being wrong.
func (h *harness) build(t *testing.T, name string, mat model.Materialisation, query string) {
	t.Helper()
	m := model.Model{Schema: h.schema, Name: name, Materialised: mat, SQL: query}

	stmts, err := h.d.Build(m, h.kindOf(t, name))
	if err != nil {
		t.Fatalf("building %s: %v", m.Ref(), err)
	}
	for _, s := range stmts {
		if err := h.conn.Exec(h.ctx, s); err != nil {
			t.Fatalf("running against %s:\n%s\n\n%v", h.d.Name(), s, err)
		}
	}
}

func (h *harness) kindOf(t *testing.T, name string) dialect.Kind {
	t.Helper()
	v, err := h.conn.Scalar(h.ctx, h.d.KindOf(h.schema+"."+name))
	if err != nil {
		t.Fatalf("asking what %s.%s is: %v", h.schema, name, err)
	}
	return dialect.KindFrom(v)
}

func (h *harness) readOne(t *testing.T, name string) any {
	t.Helper()
	v, err := h.conn.Scalar(h.ctx, fmt.Sprintf("SELECT n FROM %s.%s", h.schema, name))
	if err != nil {
		t.Fatalf("reading %s.%s: %v", h.schema, name, err)
	}
	return v
}

func (h *harness) schemaIsMade(t *testing.T) {
	// EnsureSchema already ran, and it has to be safe to run again: a build
	// runs it before every model of that schema.
	for _, s := range h.d.EnsureSchema(h.schema) {
		if err := h.conn.Exec(h.ctx, s); err != nil {
			t.Fatalf("running EnsureSchema a second time: %v", err)
		}
	}
}

func (h *harness) viewIsBuilt(t *testing.T) {
	h.build(t, "a_view", model.View, "SELECT 1 AS n")
	if got := h.kindOf(t, "a_view"); got != dialect.View {
		t.Fatalf("it is %q and the header said view", got)
	}
	if n := fmt.Sprint(h.readOne(t, "a_view")); n != "1" {
		t.Errorf("it reads %s, want 1", n)
	}
}

func (h *harness) viewIsIdempotent(t *testing.T) {
	h.build(t, "again", model.View, "SELECT 7 AS n")
	h.build(t, "again", model.View, "SELECT 7 AS n")
	if n := fmt.Sprint(h.readOne(t, "again")); n != "7" {
		t.Errorf("it reads %s, want 7", n)
	}
}

func (h *harness) tableIsBuilt(t *testing.T) {
	h.build(t, "a_table", model.Table, "SELECT 2 AS n")
	if got := h.kindOf(t, "a_table"); got != dialect.Table {
		t.Fatalf("it is %q and the header said table", got)
	}
	if n := fmt.Sprint(h.readOne(t, "a_table")); n != "2" {
		t.Errorf("it reads %s, want 2", n)
	}
}

// THE ONE THAT SAYS THE TWO WORDS MEAN ANYTHING.
//
// `view` and `table` both answer a SELECT, so every test above passes on a
// dialect that builds views for both. The difference is what happens when
// the thing underneath changes: a table holds the rows it was built from,
// and a view goes and looks again.
//
// Built out of this package's own primitives rather than a seeded source
// table, so it needs no warehouse-specific DDL and runs identically on the
// next dialect.
func (h *harness) tableHoldsRowsAViewDoesNot(t *testing.T) {
	src := fmt.Sprintf("%s.src", h.schema)

	h.build(t, "src", model.View, "SELECT 10 AS n")
	h.build(t, "held", model.Table, "SELECT n FROM "+src)
	h.build(t, "looked", model.View, "SELECT n FROM "+src)

	// The ground moves.
	h.build(t, "src", model.View, "SELECT 20 AS n")

	if n := fmt.Sprint(h.readOne(t, "held")); n != "10" {
		t.Errorf("the table reads %s: it did not HOLD the rows it was built "+
			"from, so `table` built something a SELECT cannot tell from a view", n)
	}
	if n := fmt.Sprint(h.readOne(t, "looked")); n != "20" {
		t.Errorf("the view reads %s: it did not look again, so `view` built "+
			"something that is not one", n)
	}
}

func (h *harness) viewReplacesATable(t *testing.T) {
	h.build(t, "swap_to_view", model.Table, "SELECT 3 AS n")
	h.build(t, "swap_to_view", model.View, "SELECT 4 AS n")
	if got := h.kindOf(t, "swap_to_view"); got != dialect.View {
		t.Fatalf("it is still %q", got)
	}
	if n := fmt.Sprint(h.readOne(t, "swap_to_view")); n != "4" {
		t.Errorf("it reads %s, want 4", n)
	}
}

func (h *harness) tableReplacesAView(t *testing.T) {
	h.build(t, "swap_to_table", model.View, "SELECT 5 AS n")
	h.build(t, "swap_to_table", model.Table, "SELECT 6 AS n")
	if got := h.kindOf(t, "swap_to_table"); got != dialect.Table {
		t.Fatalf("it is still %q", got)
	}
	if n := fmt.Sprint(h.readOne(t, "swap_to_table")); n != "6" {
		t.Errorf("it reads %s, want 6", n)
	}
}

// Absent is an ANSWER, not a failure. Build is given it on every first
// build, so a dialect returning an error here would make a fresh project
// unbuildable.
func (h *harness) absentIsAbsent(t *testing.T) {
	if got := h.kindOf(t, "never_made"); got != dialect.Absent {
		t.Errorf("a relation that was never made is %q", got)
	}
}

// A STRING LITERAL IS NOT THE SAME IN BOTH, which is why Literal is on the
// interface at all. Measured on 2026-10-08, with one backslash in the SQL:
//
//	postgres   SELECT LENGTH('a\bc')  ->  4   the backslash is a character
//	bigquery   SELECT LENGTH('a\bc')  ->  3   \b is a backspace
//
// So a value a consumer wrote in `accepted_values` means two different
// things depending on where it runs, and the test built from it would pass
// against data it should refuse. These are the characters that do it.
func (h *harness) literalsSurvive(t *testing.T) {
	for _, want := range []string{
		`plain`,
		`it's quoted`,
		`back\slash`,
		`both ' and \ at once`,
		"a\nb", // a real line break: Postgres takes one inside a literal
		"a\tb", // and a real tab
		`%_`,   // nothing here is a LIKE, and they must not become one
	} {
		got, err := h.conn.Scalar(h.ctx, "SELECT "+h.d.Literal(want))
		if err != nil {
			t.Errorf("%q: %v  (as %s)", want, err, h.d.Literal(want))
			continue
		}
		if fmt.Sprint(got) != want {
			t.Errorf("sent %q as %s and the warehouse read %q",
				want, h.d.Literal(want), fmt.Sprint(got))
		}
	}
}

// THE CATALOG IDENTITY, which is what makes a model appear on `/data`
// rather than only in a log. The engine takes it on a `landed` line and
// refuses anything that does not match its own rule -- SILENTLY, counting
// the refusal rather than failing the step, because a target the engine
// repaired would be a target the engine inferred. So a wrong one here is a
// model that builds, reports success, and never appears. That is what this
// asserts, and it is asserted against the SHAPE rather than by importing
// the engine, which is another module.
func (h *harness) targetNamesTheRelation(t *testing.T) {
	const name = "a_view"
	got := h.conn.Target(h.schema + "." + name)

	scheme, rest, ok := strings.Cut(got, "://")
	if !ok || scheme == "" {
		t.Fatalf("%q has no scheme", got)
	}
	if strings.ToLower(scheme) != scheme {
		t.Errorf("%q: the engine refuses a scheme that is not lower-case", got)
	}
	segs := strings.Split(rest, "/")
	if len(segs) != 3 {
		t.Fatalf("%q has %d path segments; a table target takes 3", got, len(segs))
	}
	if segs[0] == "" {
		t.Errorf("%q: the first segment is the project or database and is empty", got)
	}
	if segs[1] != h.schema || segs[2] != name {
		t.Errorf("%q does not end in %s/%s", got, h.schema, name)
	}

	// NOTHING OF THE DSN. A connection string carries a password, and this
	// string goes into a catalog, onto a screen and into a primary key. The
	// engine refuses '@' for exactly this reason -- "contains '@', which
	// only a DSN would" -- so getting it wrong is silent again.
	for _, bad := range []string{"@", "?", "#", " "} {
		if strings.Contains(got, bad) {
			t.Errorf("%q contains %q, which the engine refuses", got, bad)
		}
	}
}

// dropSchema is best effort: a left-over schema costs a name, and failing
// the test over the cleanup would hide whatever the test actually found.
func dropSchema(d dialect.Dialect, conn dialect.Conn, schema string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CASCADE is right HERE and nowhere else in this package: the schema was
	// made by this run, under a name nothing else can be using.
	//
	// The one SQL literal in a dialect-agnostic file. `DROP SCHEMA ...
	// CASCADE` is standard and both warehouses this suite is written for
	// take it, so it buys a DropSchema on the interface that nothing but
	// this cleanup would ever call.
	_ = conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
}
