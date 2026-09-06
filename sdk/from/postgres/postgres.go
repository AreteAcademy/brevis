// Package postgres reads records out of PostgreSQL.
//
// It imports pgx. A fetcher that reads HTTP or files never compiles it -- the
// same rule that took BigQuery out of `to`, and it is measured: a files-only
// consumer was compiling 461 packages and 21 MB because of a driver it did not
// use.
package postgres

import (
	"context"
	"fmt"
	"iter"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Query reads the result of a SELECT, one row per record.
//
//	From: postgres.Query{
//	    DSN: os.Getenv("PG_DSN"),
//	    SQL: "SELECT * FROM orders WHERE updated_at > $1 ORDER BY updated_at, id LIMIT $2",
//	    Args: []any{run.LogicalDate, 50_000},
//	}
//
// The rows are delivered as a stream, one at a time: the driver never builds
// the whole list before returning. On a table of millions of rows the
// difference is between running and running out of memory.
type Query struct {
	// DSN is the connection string. Required.
	//
	//	postgres://user:password@host:5432/database?sslmode=require
	//
	// It carries a password, so it never appears in a log nor in Describe().
	DSN string

	// SQL is the query. Required.
	//
	// Paginate by KEY and not by OFFSET: `WHERE id > $1 ORDER BY id LIMIT $2`.
	// OFFSET on a large table is O(n^2), because the server counts the
	// discarded rows on every page -- which is why this driver offers no
	// Offset field, that would only invite the mistake.
	SQL string

	// Args are the parameters for $1, $2... Optional.
	Args []any

	// FetchSize is how many rows the server sends per round trip. Zero uses
	// pgx's default. It trades memory for latency on a wide query.
	FetchSize int

	// Timeout bounds the whole query. Zero means no bound -- a legitimately
	// long SELECT must not die on a number the SDK invented.
	Timeout time.Duration

	// Conn reuses a connection you already have. Nil opens one and closes it
	// at the end.
	Conn *pgx.Conn
}

// Describe satisfies core.Reader. It says the query, never the DSN: the DSN
// carries a password, and Describe reaches logs and error messages.
func (q Query) Describe() string { return "postgres: " + firstLine(q.SQL) }

// Read satisfies core.Reader.
func (q Query) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	if q.DSN == "" && q.Conn == nil {
		return nil, fmt.Errorf("postgres.Query needs DSN (or Conn)")
	}
	if q.SQL == "" {
		return nil, fmt.Errorf("postgres.Query needs SQL")
	}

	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		_ = cancel // the context dies with the iteration; see the defer below
	}

	conn, closeConn, err := q.connect(ctx)
	if err != nil {
		return nil, err
	}

	// The query is fired HERE, and not inside the iterator, so that a wrong
	// DSN, a missing table or invalid SQL come back as an Extract error -- and
	// not as the first item of a sequence the caller has to drain to find out.
	// It is the same reason HTTP fetches the first page early.
	rows, err := conn.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		closeConn()
		return nil, fmt.Errorf("postgres: %w", err)
	}

	start := time.Now()
	return func(yield func(core.Envelope, error) bool) {
		defer closeConn()
		defer rows.Close()

		// The name and the OID come together: the OID is the column's DECLARED
		// type, and the conversion comes out of it. Without it DATE and
		// TIMESTAMPTZ arrive as the same time.Time and the DATE gains an
		// invented time.
		fields := rows.FieldDescriptions()
		names := make([]string, len(fields))
		oids := make([]uint32, len(fields))
		for i, c := range fields {
			names[i], oids[i] = c.Name, c.DataTypeOID
		}

		count := 0
		var sample []any

		defer func() {
			if opt.Stats != nil {
				opt.Stats.Pages = 1
				opt.Stats.Attempts = 1
			}
			elapsed := time.Since(start)
			core.LogExtract(ctx, "postgres", q.Describe(), core.PreviewStats{
				Rows: count, Pages: 1, Duration: elapsed,
			})
			if opt.Preview > 0 {
				core.WritePreview(opt.PreviewWriter, sample, opt.PreviewBytes, core.PreviewStats{
					Rows: count, Pages: 1, Duration: elapsed,
				})
			}
		}()

		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				yield(core.Envelope{}, fmt.Errorf("postgres: reading row %d: %w", count+1, err))
				return
			}

			record := make(map[string]any, len(names))
			for i, name := range names {
				record[name] = ToJSONWithOID(values[i], oids[i])
			}

			count++
			if opt.Preview > 0 && len(sample) < opt.Preview {
				sample = append(sample, record)
			}
			if !yield(core.Envelope{Payload: record}, nil) {
				return
			}
		}

		// rows.Err() only carries a value AFTER the loop, and an error here
		// means the query died halfway -- which the caller has to see, or it
		// loads half a batch believing it loaded all of it.
		if err := rows.Err(); err != nil {
			yield(core.Envelope{}, fmt.Errorf("postgres: after %d row(s): %w", count, err))
		}
	}, nil
}

func (q Query) connect(ctx context.Context) (*pgx.Conn, func(), error) {
	if q.Conn != nil {
		return q.Conn, func() {}, nil
	}
	cfg, err := pgx.ParseConfig(q.DSN)
	if err != nil {
		// pgx's error can contain the DSN, and the DSN carries a password.
		return nil, nil, fmt.Errorf("postgres: DSN is not valid")
	}
	if q.FetchSize > 0 {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: connecting: %w", redact(err, q.DSN))
	}
	return conn, func() { _ = conn.Close(context.WithoutCancel(ctx)) }, nil
}
