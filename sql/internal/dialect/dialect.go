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
