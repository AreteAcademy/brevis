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

	// Build is every statement that turns the model into what its header
	// says, in order, given what is in the warehouse NOW.
	Build(m model.Model, current Kind) ([]string, error)
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
