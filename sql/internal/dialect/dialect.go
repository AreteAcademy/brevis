// Package dialect is the part of building a model that differs per warehouse.
//
// ONE INTERFACE WITH A CONFORMANCE SUITE, written while there was a single
// implementation. That order is the whole point: a second dialect added
// against an interface nobody tests agrees with the first wherever the author
// happened to look and differs everywhere else, and the difference is found
// by a consumer. Here it is found by `go test`.
//
// What is NOT in here is as deliberate as what is. There is no "quote an
// identifier", because this package refuses a name that would need quoting
// instead -- see the Postgres dialect's Build for why. There is no "begin a
// transaction", because BigQuery does not put DDL in one and an interface
// that lies about that would be worse than two honest orderings.
package dialect

import (
	"context"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/model"
)

// Kind is what a relation IS in the warehouse right now.
//
// Build is given it, because "create or replace" is three different
// statements depending on the answer and every dialect has to ask.
type Kind string

const (
	// Absent is nothing of that name.
	Absent Kind = ""
	Table  Kind = "table"
	View   Kind = "view"
)

// Conn is the little a warehouse has to do.
//
// Two methods, and they are the two the build loop needs: run a statement,
// and read one value back. A driver's own richer surface stays behind its
// dialect, where it belongs -- the loop must not learn what a pgx.Rows is.
type Conn interface {
	// Exec runs one statement. DDL, so nothing is returned.
	Exec(ctx context.Context, statement string) error

	// Scalar runs a query of one row and one column. A query matching no row
	// returns nil, nil -- that is "nothing is there", which is an ANSWER on
	// this path and not a failure.
	Scalar(ctx context.Context, query string) (any, error)

	// Target names a relation the way the CATALOG does:
	// `bigquery://project/dataset/table`, `postgres://database/schema/table`.
	//
	// ON Conn AND NOT ON Dialect, because only a live connection knows the
	// first segment: the project for BigQuery, the database for Postgres,
	// and the latter is inside a DSN this package never sees in one piece.
	//
	// It is what puts a model on `/data`. The engine takes it on a `landed`
	// line and refuses anything that does not match its own rule -- counting
	// the refusal rather than failing the step, deliberately, because a
	// target the engine repaired would be a target the engine inferred. A
	// wrong one here is therefore a model that builds, reports success and
	// never appears, which is why the conformance suite asserts its shape.
	Target(ref string) string

	Close(ctx context.Context) error
}

// Dialect is one warehouse's half of building a model.
type Dialect interface {
	// Name is what `--dialect` is given, and it is also the name the
	// reference extractor is given: a project cannot read its SQL as one
	// dialect and build it as another.
	Name() string

	// Open connects. The DSN comes from an environment variable named in the
	// command line, never from a file -- a connection string carries a
	// password and a project directory is in git.
	Open(ctx context.Context, dsn string) (Conn, error)

	// EnsureSchema is run before the models of that schema, and has to be
	// safe to run when the schema is already there.
	EnsureSchema(schema string) []string

	// KindOf is a query whose single value is "table", "view" or NULL.
	KindOf(ref string) string

	// Literal is a Go string as this warehouse's SQL spells it, quotes
	// included.
	//
	// ON THE INTERFACE BECAUSE IT MEASURABLY DIFFERS, and for no other
	// reason -- with one backslash in the statement, on 2026-10-08:
	//
	//	postgres   SELECT LENGTH('a\bc')  ->  4   a backslash is a character
	//	bigquery   SELECT LENGTH('a\bc')  ->  3   \b is a backspace
	//
	// A value a consumer wrote in an `accepted_values` test therefore means
	// two different things depending on where the project runs, and the
	// check built from it would pass against data it should refuse.
	Literal(s string) string

	// ColumnsOf is a query whose single value is the relation's column
	// names, in ordinal order, comma-separated.
	//
	// ONE VALUE AND NOT A ROW SET, so Conn keeps its two methods. `MERGE ...
	// WHEN MATCHED THEN UPDATE SET` has to name every column -- BigQuery has
	// no `SET *`, measured 2026-10-08 -- so the list has to be asked for, and
	// `STRING_AGG(column_name, ',' ORDER BY ordinal_position)` is what both
	// warehouses answer it with. A third Conn method for one caller would
	// have been the thing this package's own comment argues against.
	ColumnsOf(ref string) string

	// Build is every statement that turns the model into what its header
	// says, in order, given what is in the warehouse NOW.
	Build(m model.Model, st State) ([]string, error)
}

// State is what the warehouse holds for this model right now, and what the
// caller asked for.
//
// A STRUCT AND NOT A THIRD ARGUMENT, which is the precedent #42 set for
// CheckRow: the questions a build has to answer grow, and a signature that
// grows with them is one every implementation and every test has to be
// edited for, one argument at a time, with the compiler unable to tell a
// swapped pair apart.
type State struct {
	// Current is what the relation IS right now.
	Current Kind

	// FullRefresh is `--full-refresh`: build it as if nothing were there.
	FullRefresh bool

	// Columns is the TARGET's column names in ordinal order, from ColumnsOf.
	//
	// Read only when it is going to be used -- an incremental model that is
	// adding to an existing table -- and nil otherwise. A list read on every
	// build would be a query per model for an answer three materialisations
	// out of four never look at.
	Columns []string
}

// Rebuild says this build creates the relation rather than adding to it.
//
// DERIVED AND NEVER STORED. Three inputs decide it, and a fourth field
// holding the answer would be a second place for it to be wrong -- which is
// exactly how a `--full-refresh` that set the flag and forgot the field would
// merge into the table it was told to replace.
func (s State) Rebuild() bool { return s.Current != Table || s.FullRefresh }

// Merge is the parts of a MERGE that are the same in every warehouse.
//
// HERE AND NOT IN EACH DIALECT, because none of it is a dialect difference:
// a join on the key columns, a SET of the ones that are not keys, and an
// INSERT naming the target's columns. What DOES differ -- how the source is
// deduplicated -- is not in here, and that is the whole split.
type Merge struct {
	// On is `t.k = s.k AND t.j = s.j`.
	On string
	// Set is `c = s.c, d = s.d`, the columns that are NOT keys.
	//
	// Empty when every column is a key. A MERGE then has nothing to update
	// and the WHEN MATCHED clause is omitted entirely -- `SET` with no
	// assignment is a syntax error in both warehouses, and "the row is
	// already exactly this row" is not a failure.
	Set string
	// Columns and Values are the INSERT's two halves, in the target's own
	// ordinal order.
	Columns string
	Values  string
}

// MergeOf builds those parts from the model's keys and the target's columns.
func MergeOf(m model.Model, columns []string) Merge {
	key := map[string]bool{}
	for _, k := range m.UniqueKey {
		key[k] = true
	}

	on := make([]string, 0, len(m.UniqueKey))
	for _, k := range m.UniqueKey {
		on = append(on, "t."+k+" = s."+k)
	}

	set := make([]string, 0, len(columns))
	vals := make([]string, 0, len(columns))
	for _, c := range columns {
		vals = append(vals, "s."+c)
		if !key[c] {
			set = append(set, c+" = s."+c)
		}
	}
	return Merge{
		On:      strings.Join(on, " AND "),
		Set:     strings.Join(set, ", "),
		Columns: strings.Join(columns, ", "),
		Values:  strings.Join(vals, ", "),
	}
}

// NewRows wraps a model's SQL in the filter that makes a build incremental.
//
// THE WATERMARK IS A SUBQUERY AND NEVER A VALUE, which is the single most
// load-bearing line in this file. BigQuery hands its own TIMESTAMP back over
// the REST API as `"1767484800.0"` -- epoch seconds, as a JSON string -- and
// feeding that back in is refused outright: `Could not cast literal
// "1767484800.0" to type TIMESTAMP`, measured 2026-10-08. pgx, for the same
// column type, returns a time.Time. So a design that read MAX(w) in Go and
// formatted it into the next statement would be two different bugs in two
// warehouses, and the quiet one is the dangerous one.
//
// AN EMPTY TARGET IS THE OTHER TRAP. MAX over no rows is NULL, and `w >
// NULL` is NULL, which admits nothing -- so a model whose first build landed
// an empty table would stay empty forever while every build reported
// success. The IS NULL arm is what stops that, and the conformance suite has
// a case for it because nothing else would ever notice.
func NewRows(m model.Model, body string) string {
	ref, w := m.Ref(), m.Watermark
	return "SELECT * FROM (\n" + body + "\n) AS brevis_new\n" +
		" WHERE (SELECT MAX(" + w + ") FROM " + ref + ") IS NULL\n" +
		"    OR " + w + " > (SELECT MAX(" + w + ") FROM " + ref + ")"
}

// Result is a result set, as a screen needs it.
//
// `[][]any` AND NOT `[][]string`, because NULL is not the empty string. One
// is "nothing was recorded" and the other is a value somebody wrote; they
// render differently and are read differently, so they have to arrive
// differently. A nil cell is NULL.
type Result struct {
	// Columns are the names, in the order the query returned them.
	Columns []string

	// Rows are the values, one slice per row, aligned with Columns.
	Rows [][]any

	// Truncated says the limit cut the answer.
	//
	// IT HAS TO BE SAID. A grid showing a thousand rows of a million, in
	// silence, is a grid that lies: somebody reads a MAX off it and is wrong,
	// and nothing on the screen told them not to.
	Truncated bool
}

// Reader is implemented by a connection that can return a result set.
//
// NOT ON Conn, for Disposable's reason one level over. `Conn` has Exec and
// Scalar -- "the two the build loop needs" -- and a build never reads a
// result set. Keeping this off the production interface means the build loop
// cannot reach it, and a dialect that cannot read is refused by the one
// caller that wants to rather than discovered at runtime.
//
// ON Conn's CONCRETE TYPE and not on Dialect: reading needs the connection,
// where ExpireSchema only needed a statement.
type Reader interface {
	// Read runs a query and returns at most Request.Limit rows.
	Read(ctx context.Context, req Request) (Result, error)
}

// Request is one read, and everything that bounds it.
//
// A STRUCT AND NOT THREE ARGUMENTS, because the two bounds are different
// KINDS of bound and the call site has to say which it means. Rows are a
// screen and bytes are money; a reader passing them positionally would
// eventually pass them the wrong way round, and one of those mistakes is
// expensive.
type Request struct {
	// Query is run exactly as written. Nothing wraps it: a wrapper changes
	// what the warehouse plans, and it would be this package quietly editing
	// a statement somebody is about to be charged for.
	Query string

	// Limit is the most rows to return.
	//
	// THE LIMIT CUTS, it does not refuse. A preview that errored because a
	// table is large would be a preview that never works on the tables
	// somebody actually has. What it must not do is cut in silence, which is
	// what Result.Truncated is for.
	Limit int

	// MaxBytes is the most the warehouse may BILL for this query. Zero is no
	// bound, and a dialect that cannot enforce one ignores it.
	//
	// IT IS NOT THE SAME LIMIT AS Limit, and the difference is the whole
	// reason it exists: BigQuery bills for bytes SCANNED, and a LIMIT does
	// not reduce them -- `SELECT * FROM t LIMIT 10` over a petabyte reads
	// the petabyte. A row ceiling bounds the screen and bounds nothing else.
	MaxBytes int64
}

// Estimator is implemented by a connection that can price a query WITHOUT
// RUNNING IT.
//
// Separate from Reader, and not a method on it, for the reason Reader is
// separate from Conn: Postgres has no such thing to offer. A warehouse that
// charges for a scan can answer this, one that charges for a machine by the
// hour cannot, and an interface that pretended otherwise would make every
// Postgres caller handle an answer that is always a guess.
//
// A refusal built on this runs BEFORE the money is spent, which is the only
// moment at which refusing is worth anything.
type Estimator interface {
	// Estimate returns the bytes the query would process.
	//
	// AN ERROR AND NEVER A ZERO when the price cannot be read. Zero is under
	// every ceiling, so a silent fallback would wave through exactly the
	// queries nobody could measure.
	Estimate(ctx context.Context, query string) (int64, error)
}

// Disposable is implemented by a dialect whose warehouse can be told to throw
// away what a TEST leaves behind.
//
// NOT ON Dialect, and that separation is the whole point. EnsureSchema is
// production -- run.go calls it for every schema a consumer's models land in
// -- so an expiry reachable from there would be a build that quietly deletes
// a customer's tables in a day. There is no sentence that makes that
// acceptable, so the capability lives where the production path cannot reach
// it by accident.
//
// It exists because a test CAN leak. The conformance suite and the runner's
// incremental test each create a dataset of their own, beside the client's
// real ones, and drop it in t.Cleanup -- which does not run after a panic, a
// SIGKILL or a laptop that sleeps. BigQuery has no dataset expiry at all,
// only `defaultTableExpirationMs` for the tables inside, so what this buys is
// a leak that EMPTIES rather than a leak that disappears. That residual is
// real and is the reason this is one small interface and not a cleanup
// framework.
//
// Postgres does not implement it: its test warehouse is a container, and
// `docker compose down` is the expiry.
type Disposable interface {
	// ExpireSchema is the statement that makes everything in this schema
	// expire. Run by a test right after EnsureSchema, and by nothing else.
	ExpireSchema(schema string) string
}

// StateOf asks the warehouse the questions a build needs answered.
//
// ONE PLACE, because two would drift and the drift would be invisible: the
// conformance harness mimics the build loop on purpose -- "every test here
// also exercises KindOf" -- and a harness that assembled the State its own
// way would pass a suite about itself. Both call this.
//
// THE COLUMNS ARE READ ONLY WHEN THEY WILL BE USED. Three materialisations
// out of four never look at them, and a query per model for an answer nobody
// reads is a round trip per model per build.
func StateOf(ctx context.Context, d Dialect, conn Conn, ref string,
	incremental, fullRefresh bool) (State, error) {

	v, err := conn.Scalar(ctx, d.KindOf(ref))
	if err != nil {
		return State{}, fmt.Errorf("asking what %s is: %w", ref, err)
	}
	st := State{Current: KindFrom(v), FullRefresh: fullRefresh}

	if !incremental || st.Rebuild() {
		return st, nil
	}
	cols, err := conn.Scalar(ctx, d.ColumnsOf(ref))
	if err != nil {
		return State{}, fmt.Errorf("asking which columns %s has: %w", ref, err)
	}
	st.Columns = splitColumns(cols)
	if len(st.Columns) == 0 {
		// A TABLE WITH NO COLUMNS CANNOT HAPPEN, and saying so is cheaper
		// than a MERGE built from an empty list -- which is syntactically
		// valid right up to `UPDATE SET` and then is not.
		return State{}, fmt.Errorf("%s is a table and the catalog named no columns for it", ref)
	}
	return st, nil
}

// splitColumns reads the one value ColumnsOf returns.
//
// An empty answer is NO columns and not one column called "", which is what
// strings.Split returns for an empty string and would put an unnamed column
// into a MERGE.
func splitColumns(v any) []string {
	var s string
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		s = fmt.Sprintf("%v", t)
	}
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// KindFrom reads what Scalar returned from a KindOf query.
//
// Here rather than in each dialect because the three answers are the same
// three everywhere, and a driver that hands back []byte where another hands
// back string is not a dialect difference -- it is a driver difference, and
// turning it into a Kind twice is how the two drift.
func KindFrom(v any) Kind {
	switch s := v.(type) {
	case nil:
		return Absent
	case string:
		return Kind(s)
	case []byte:
		return Kind(s)
	default:
		return Absent
	}
}
