package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	sdkpg "github.com/AreteAcademy/brevis/sdk/from/postgres"

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

	fields := rows.FieldDescriptions()
	out := dialect.Result{Rows: [][]any{}}
	for _, f := range fields {
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
		// THE SDK'S CONVERSION AND NOT A SECOND ONE. pgx hands a UUID over
		// as [16]byte, a DATE and a TIMESTAMPTZ as the same time.Time, and
		// a NUMERIC as a struct -- measured against a real server, where
		// this reader drew a UUID as a list of sixteen integers.
		//
		// `sdk/from/postgres` had already learned every one of those, with
		// a comment per case, because it has been reading Postgres for
		// longer than this has. Two readers on pgx in one repository is one
		// too many, and the one that knows is the one to ask.
		//
		// It is the ONLY thing taken from the SDK, and the weight gate says
		// so by name: a leaf under `from/` is a reader, `to/` is a write
		// path and stays forbidden.
		for i := range vals {
			vals[i] = sdkpg.ToJSONWithOID(vals[i], fields[i].DataTypeOID)
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

// Relations lists everything a SELECT could name. See dialect.Lister.
//
// ONE QUERY AND NO COST TO SPEAK OF: Postgres answers this out of its own
// catalog, which is already in memory. The 10 MiB floor that shapes the
// interface is BigQuery's, and a dialect pays what its warehouse charges.
//
// `pg_catalog` and not `information_schema`: the latter hides relations the
// current role cannot access, which would silently answer a different
// question from "what is there" -- and the roles that run this are read-only
// by design, so the filtering would be exactly wrong.
func (c *conn) Relations(ctx context.Context) ([]dialect.Relation, error) {
	rows, err := c.c.Query(ctx, `
		SELECT n.nspname, c.relname
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r', 'v', 'm', 'p', 'f')
		   AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		   AND n.nspname NOT LIKE 'pg_toast%'
		   AND n.nspname NOT LIKE 'pg_temp%'
		 ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dialect.Relation
	for rows.Next() {
		var r dialect.Relation
		if err := rows.Scan(&r.Schema, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Columns says what one relation holds. See dialect.Describer.
//
// `pg_catalog` AND NOT `information_schema`, for the second of the two
// reasons the dialect's own ColumnsOf gives for choosing the other one. That
// query is asked about a table THIS PROJECT BUILT; this one is asked about
// anything a listing returned, and a listing comes from pg_class -- which
// includes materialised views, invisible in information_schema. Answering
// "no columns" for a relation the tree just drew would be the worst possible
// answer, which is exactly the argument KindOf already makes.
//
// It also hides nothing the role cannot see, for Relations' reason: the
// roles that run this are read-only by design, so information_schema's
// privilege filtering would answer a different question from "what is
// there".
//
// THE NAMES ARE PARAMETERS. A dialect pays what its warehouse charges and
// uses what its driver gives: here that is `$1`/`$2`, so nothing is
// interpolated and no name has to be refused for being exotic.
func (c *conn) Columns(ctx context.Context, r dialect.Relation) ([]dialect.Column, error) {
	rows, err := c.c.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod)
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2
		   AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, r.Schema, r.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dialect.Column
	for rows.Next() {
		var col dialect.Column
		if err := rows.Scan(&col.Name, &col.Type); err != nil {
			return nil, err
		}
		out = append(out, col)
	}
	return out, rows.Err()
}
