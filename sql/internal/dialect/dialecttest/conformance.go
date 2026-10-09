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
	"strconv"
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
	if err := MakeThrowawaySchema(ctx, d, conn, schema); err != nil {
		t.Fatalf("%s: %v", d.Name(), err)
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
	t.Run("a table names its columns in order", h.columnsAreNamedInOrder)

	// INCREMENTAL, and the order matters: each case builds on the one
	// before, over one source table, the way a project does over days.
	t.Run("an incremental model's first build holds every row", h.incrementalFirstBuild)
	t.Run("a build with nothing new changes nothing", h.incrementalNothingNew)
	t.Run("an empty target still takes the first rows", h.incrementalFromEmpty)
	t.Run("a build with a new row adds only that row", h.incrementalAddsTheNewRow)
	t.Run("a changed row is updated and the count does not grow", h.incrementalUpdatesInPlace)
	t.Run("a duplicate key in the source does not fail the build", h.incrementalDeduplicates)
	t.Run("a full refresh rebuilds and forgets the watermark", h.incrementalFullRefresh)
	t.Run("an incremental model replaces a view of that name", h.incrementalReplacesAView)
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

	st, err := dialect.StateOf(h.ctx, h.d, h.conn, m.Ref(),
		m.Materialised == model.Incremental, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	stmts, err := h.d.Build(m, st)
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

// columnsAreNamedInOrder is ColumnsOf against a real catalog.
//
// THE ORDER IS THE CLAIM, not the set. The list feeds `MERGE ... WHEN
// MATCHED THEN UPDATE SET`, and a dialect whose query returns the right
// names in whatever order the scan produced would write a different
// statement on two runs of one model -- green here if this only checked
// membership, and a diff nobody can explain in a warehouse's query log.
//
// `one, two, three` is deliberate: alphabetical order and ordinal order
// disagree on it, so a query that forgot its ORDER BY and happened to get
// sorted names still fails.
func (h *harness) columnsAreNamedInOrder(t *testing.T) {
	h.build(t, "cols", model.Table, "SELECT 1 AS one, 2 AS two, 3 AS three")

	v, err := h.conn.Scalar(h.ctx, h.d.ColumnsOf(h.schema+".cols"))
	if err != nil {
		t.Fatalf("asking for the columns: %v", err)
	}
	got := asString(v)
	if got != "one,two,three" {
		t.Errorf("columns = %q, wanted \"one,two,three\"", got)
	}

	// AND NOTHING FOR WHAT IS NOT THERE. The caller asks this only about a
	// relation KindOf just reported, so the answer never has to be guessed
	// at -- but a dialect returning an empty string instead of nothing would
	// make `strings.Split` produce a one-element list holding "", and the
	// MERGE would name a column called the empty string.
	v, err = h.conn.Scalar(h.ctx, h.d.ColumnsOf(h.schema+".no_such_relation"))
	if err != nil {
		t.Fatalf("asking about something absent: %v", err)
	}
	if v != nil && asString(v) != "" {
		t.Errorf("something absent answered %v", v)
	}
}

// asInt reads a COUNT(*) back. Postgres hands an int64, the BigQuery REST
// path hands the digits as a string -- the same driver split asString and
// dialect.KindFrom exist for.
func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	case []byte:
		i, _ := strconv.ParseInt(string(n), 10, 64)
		return i
	}
	return 0
}

// asString reads what a driver handed back for a text column. pgx gives a
// string, the BigQuery REST path gives whatever the JSON held -- the same
// split dialect.KindFrom exists for, for the same reason.
func asString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return fmt.Sprintf("%v", s)
	}
}

// ---------------------------------------------------------------------------
// Incremental.
//
// SEVEN CASES OVER ONE SOURCE TABLE, in order, each standing on the one
// before -- which is how a project meets this: the same model, the same
// table, a different day. A case that reset the world between runs would
// test the first build seven times.
//
// The source is built THROUGH the dialect, as a table model, so this file
// writes no warehouse-specific DDL of its own. `incSource` replaces it;
// Postgres drops and recreates, BigQuery replaces in one statement, and
// neither fact belongs here.

const incModel = "SELECT k, v, w FROM %s.inc_src"

// incSource replaces the source table with exactly these rows.
func (h *harness) incSource(t *testing.T, rows string) {
	t.Helper()
	h.build(t, "inc_src", model.Table, rows)
}

// runModel builds one model the way the runner does, State and all.
func (h *harness) runModel(t *testing.T, m model.Model, fullRefresh bool) {
	t.Helper()
	st, err := dialect.StateOf(h.ctx, h.d, h.conn, m.Ref(),
		m.Materialised == model.Incremental, fullRefresh)
	if err != nil {
		t.Fatalf("%v", err)
	}
	stmts, err := h.d.Build(m, st)
	if err != nil {
		t.Fatalf("building %s: %v", m.Ref(), err)
	}
	for _, s := range stmts {
		if err := h.conn.Exec(h.ctx, s); err != nil {
			t.Fatalf("running against %s:\n%s\n\n%v", h.d.Name(), s, err)
		}
	}
}

// incBuild builds the suite's own incremental model.
func (h *harness) incBuild(t *testing.T, fullRefresh bool) {
	t.Helper()
	h.runModel(t, model.Model{
		Schema: h.schema, Name: "inc", Materialised: model.Incremental,
		UniqueKey: []string{"k"}, Watermark: "w",
		SQL: fmt.Sprintf(incModel, h.schema),
	}, fullRefresh)
}

// countOf is how many rows a relation holds.
func (h *harness) countOf(t *testing.T, name string) int64 {
	t.Helper()
	v, err := h.conn.Scalar(h.ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.%s", h.schema, name))
	if err != nil {
		t.Fatalf("counting %s: %v", name, err)
	}
	return asInt(v)
}

func (h *harness) incRows(t *testing.T) int64 { return h.countOf(t, "inc") }

// incValue is one row's v, by key.
func (h *harness) incValue(t *testing.T, k int) string {
	t.Helper()
	v, err := h.conn.Scalar(h.ctx,
		fmt.Sprintf("SELECT v FROM %s.inc WHERE k = %d", h.schema, k))
	if err != nil {
		t.Fatalf("reading k=%d: %v", k, err)
	}
	return asString(v)
}

// The literals are spelled once, here, because both warehouses take this
// exact form and a case that spelled its own would be a case about spelling.
func row(k int, v, w string) string {
	return fmt.Sprintf("SELECT %d AS k, '%s' AS v, TIMESTAMP '%s' AS w", k, v, w)
}

func rows(parts ...string) string { return strings.Join(parts, "\nUNION ALL\n") }

// A FIRST BUILD IS A FULL BUILD, and it lands a TABLE. Nothing is there to
// filter against, so the whole model runs -- and if it landed a view there
// would be nothing to merge into tomorrow.
func (h *harness) incrementalFirstBuild(t *testing.T) {
	h.incSource(t, rows(
		row(1, "a", "2026-01-01 00:00:00"),
		row(2, "b", "2026-01-02 00:00:00"),
	))
	h.incBuild(t, false)

	if got := h.kindOf(t, "inc"); got != dialect.Table {
		t.Fatalf("an incremental model landed as %q, not a table", got)
	}
	if n := h.incRows(t); n != 2 {
		t.Errorf("%d rows after the first build, wanted 2", n)
	}
}

// NOTHING NEW CHANGES NOTHING. The whole point: a second build of an
// unchanged source is not a rebuild, and a dialect that re-ran the model in
// full would pass every count here and cost the money this materialisation
// exists to save.
func (h *harness) incrementalNothingNew(t *testing.T) {
	h.incBuild(t, false)
	if n := h.incRows(t); n != 2 {
		t.Errorf("%d rows, wanted the same 2", n)
	}
	if v := h.incValue(t, 1); v != "a" {
		t.Errorf("k=1 is %q, wanted a", v)
	}
}

// AN EMPTY TARGET STILL FILLS. MAX over no rows is NULL and `w > NULL` is
// NULL, which admits nothing -- so without the IS NULL arm a model whose
// first build landed an empty table stays empty forever, while every build
// reports success and every count is the one the last build left.
//
// It happens on an ordinary day: the source had nothing the first time.
// Nothing else in this suite would ever notice, which is the reason this
// case is here and not a comment.
func (h *harness) incrementalFromEmpty(t *testing.T) {
	// ITS OWN SOURCE, so this case does not disturb the ordered ones above
	// and can be run alone. `WHERE 1 = 0` over a subquery rather than over
	// nothing: BigQuery wants a FROM before a WHERE.
	none := "SELECT k, v, w FROM (" + row(1, "x", "2026-01-01 00:00:00") + ") AS z WHERE 1 = 0"
	h.build(t, "inc_e_src", model.Table, none)
	h.build(t, "inc_empty", model.Table, "SELECT k, v, w FROM "+h.schema+".inc_e_src")
	if n := h.countOf(t, "inc_empty"); n != 0 {
		t.Fatalf("the fixture holds %d rows and should hold none", n)
	}

	h.build(t, "inc_e_src", model.Table, rows(
		row(1, "a", "2026-01-01 00:00:00"),
		row(2, "b", "2026-01-02 00:00:00"),
	))
	h.runModel(t, model.Model{
		Schema: h.schema, Name: "inc_empty", Materialised: model.Incremental,
		UniqueKey: []string{"k"}, Watermark: "w",
		SQL: "SELECT k, v, w FROM " + h.schema + ".inc_e_src",
	}, false)

	if n := h.countOf(t, "inc_empty"); n != 2 {
		t.Errorf("%d rows, wanted 2 -- an empty target admitted nothing", n)
	}
}

// A NEW ROW, and only it. The source gains a third row with a later
// watermark; the two already there are unchanged and must not be processed
// again.
func (h *harness) incrementalAddsTheNewRow(t *testing.T) {
	h.incSource(t, rows(
		row(1, "a", "2026-01-01 00:00:00"),
		row(2, "b", "2026-01-02 00:00:00"),
		row(3, "c", "2026-01-03 00:00:00"),
	))
	h.incBuild(t, false)

	if n := h.incRows(t); n != 3 {
		t.Fatalf("%d rows, wanted 3", n)
	}
	if v := h.incValue(t, 3); v != "c" {
		t.Errorf("k=3 is %q, wanted c", v)
	}
}

// A CHANGED ROW IS UPDATED IN PLACE. This is what `unique_key` buys, and the
// count is the assertion: an INSERT would also make k=1 readable as "A" --
// by putting a second row next to the first and letting the reader pick.
//
// The changed row carries a LATER watermark, which is not a detail of the
// test: a watermark is how the model knows a row is new, so a correction
// written with yesterday's timestamp is a correction this materialisation
// cannot see. That is the bargain, and it is why `lookback` exists elsewhere.
func (h *harness) incrementalUpdatesInPlace(t *testing.T) {
	h.incSource(t, rows(
		row(1, "A", "2026-01-04 00:00:00"),
		row(2, "b", "2026-01-02 00:00:00"),
		row(3, "c", "2026-01-03 00:00:00"),
	))
	h.incBuild(t, false)

	if n := h.incRows(t); n != 3 {
		t.Fatalf("%d rows after an update, wanted 3 -- it was inserted, not merged", n)
	}
	if v := h.incValue(t, 1); v != "A" {
		t.Errorf("k=1 is %q, wanted A", v)
	}
}

// A DUPLICATE KEY IN THE SOURCE MUST NOT FAIL THE BUILD.
//
// Both warehouses refuse a MERGE whose source holds two rows for one target
// row -- BigQuery "must match at most one source row for each target row",
// Postgres 17 "cannot affect row a second time", measured 2026-10-08. So the
// dialect deduplicates, latest by watermark, and the proof is that this runs
// AND that the surviving row is the later one.
//
// The two spellings differ and that is the point of asking it here: BigQuery
// has QUALIFY, Postgres has DISTINCT ON and no QUALIFY at all.
func (h *harness) incrementalDeduplicates(t *testing.T) {
	h.incSource(t, rows(
		row(1, "A", "2026-01-04 00:00:00"),
		row(2, "b", "2026-01-02 00:00:00"),
		row(3, "c", "2026-01-03 00:00:00"),
		row(4, "early", "2026-01-05 00:00:00"),
		row(4, "late", "2026-01-06 00:00:00"),
	))
	h.incBuild(t, false)

	if n := h.incRows(t); n != 4 {
		t.Fatalf("%d rows, wanted 4 -- the duplicate was not collapsed", n)
	}
	if v := h.incValue(t, 4); v != "late" {
		t.Errorf("k=4 is %q, wanted late -- the surviving row is not the latest", v)
	}
}

// A FULL REFRESH FORGETS EVERYTHING, including rows the source no longer
// has. An incremental build never removes a row; this is the only thing that
// does, and that difference is the reason the flag exists.
func (h *harness) incrementalFullRefresh(t *testing.T) {
	h.incSource(t, row(9, "only", "2026-01-01 00:00:00"))
	h.incBuild(t, true)

	if n := h.incRows(t); n != 1 {
		t.Fatalf("%d rows after a full refresh, wanted 1", n)
	}
	if v := h.incValue(t, 9); v != "only" {
		t.Errorf("k=9 is %q, wanted only", v)
	}
}

// A VIEW OF THAT NAME IS REPLACED. A model that was a view yesterday has no
// rows to add to: there is nothing to merge into, and `Rebuild()` says so.
func (h *harness) incrementalReplacesAView(t *testing.T) {
	h.build(t, "inc_v", model.View, "SELECT 1 AS k, 'x' AS v, TIMESTAMP '2026-01-01 00:00:00' AS w")
	if got := h.kindOf(t, "inc_v"); got != dialect.View {
		t.Fatalf("the fixture is a %q, not a view", got)
	}

	m := model.Model{
		Schema: h.schema, Name: "inc_v", Materialised: model.Incremental,
		UniqueKey: []string{"k"}, Watermark: "w",
		SQL: fmt.Sprintf(incModel, h.schema),
	}
	st, err := dialect.StateOf(h.ctx, h.d, h.conn, m.Ref(), true, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !st.Rebuild() {
		t.Fatal("a view is not a thing to merge into, and Rebuild() said it was")
	}
	h.runModel(t, m, false)
	if got := h.kindOf(t, "inc_v"); got != dialect.Table {
		t.Errorf("it is still a %q", got)
	}
}
