package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// Open connects.
//
// THE SECOND REQUIRE THIS MODULE HAS, and it is the one go.mod's own comment
// said would have to argue for itself. It does: S4 of #62 is "against a real
// Postgres", a dialect that cannot connect cannot be conformance-tested, and
// a test suite that proves only the strings is the thing this package exists
// to avoid. pgx is what the SDK already uses, so the repository has one
// Postgres driver rather than two opinions about escaping.
func (Dialect) Open(ctx context.Context, dsn string) (dialect.Conn, error) {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return &conn{c}, nil
}

type conn struct{ c *pgx.Conn }

func (c *conn) Exec(ctx context.Context, statement string) error {
	_, err := c.c.Exec(ctx, statement)
	return err
}

// Scalar reads one value, and NO ROW IS AN ANSWER.
//
// "nothing of that name is there" is what KindOf returns for every model's
// first build, so turning it into an error would make a fresh project
// unbuildable. Values() rather than Scan(&any): pgx decides the Go type from
// the column's OID, and dialect.KindFrom takes it from there -- which is
// also why a driver handing back []byte where another hands back string is
// not this file's problem.
func (c *conn) Scalar(ctx context.Context, query string) (any, error) {
	rows, err := c.c.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, nil
	}
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("the query returned a row with no columns: %s", query)
	}
	return vals[0], rows.Err()
}

// Read runs a query and returns at most req.Limit rows. See dialect.Reader.
//
// THE STATEMENT IS RUN EXACTLY AS WRITTEN. Nothing wraps it in a subquery
// with a LIMIT: a wrapper changes what the planner does, and it would be this
// package quietly editing SQL somebody typed. The cap is applied HERE, on the
// rows as they stream back, which is the one place it can be applied without
// touching the query.
//
// That matters more than it does on BigQuery. A preview composes its own
// statement and carries a LIMIT; a query somebody typed may carry none at
// all, and this is then the only thing between a `SELECT * FROM events` and
// a million rows crossing the wire.
//
// req.MaxBytes IS IGNORED, and the interface says a dialect may: Postgres
// charges for a machine by the hour and has nothing to bill for a scan, so
// there is no bound here to set. Its limits are the row ceiling and the
// clock.
func (c *conn) Read(ctx context.Context, req dialect.Request) (dialect.Result, error) {
	rows, err := c.c.Query(ctx, req.Query)
	if err != nil {
		return dialect.Result{}, err
	}
	defer rows.Close()

	out := dialect.Result{Rows: [][]any{}}
	for _, f := range rows.FieldDescriptions() {
		out.Columns = append(out.Columns, f.Name)
	}
	for len(out.Rows) < req.Limit && rows.Next() {
		// Values() rather than Scan(&any): pgx decides the Go type from the
		// column's OID, which is what keeps a NULL arriving as nil instead
		// of as an empty string -- the one distinction the grid cannot
		// recover later.
		vals, err := rows.Values()
		if err != nil {
			return dialect.Result{}, err
		}
		out.Rows = append(out.Rows, vals)
	}
	// STOPPING EARLY IS NOT AN ERROR. The cursor is closed by the defer and
	// the server drops the rest; rows.Err() is still asked, because a
	// connection that died mid-read must not look like a short answer.
	if err := rows.Err(); err != nil {
		return dialect.Result{}, err
	}
	return out, nil
}

// Target is `postgres://database/schema/table`.
//
// THE DATABASE AND NOTHING ELSE OF THE DSN. The connection string carries a
// user and a password, and this string goes into a catalog, onto a screen
// and into a primary key -- the engine refuses a target containing '@' for
// exactly that reason. pgx has already parsed the DSN, so the name is read
// from the config rather than from the string.
//
// A ref with no dot cannot happen -- project.Load builds every one from
// `models/<schema>/<name>.sql` -- and if it ever did, an empty schema
// segment is refused by the engine rather than guessed at here.
func (c *conn) Target(ref string) string {
	schema, name, _ := strings.Cut(ref, ".")
	return "postgres://" + c.c.Config().Database + "/" + schema + "/" + name
}

func (c *conn) Close(ctx context.Context) error { return c.c.Close(ctx) }
