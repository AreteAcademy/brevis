// Package mysql reads records out of MySQL.
//
// It imports the MySQL driver on top of database/sql. A fetcher that reads
// HTTP, files or Postgres never compiles it.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" driver

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Query reads the result of a SELECT, one row per record.
//
//	From: mysql.Query{
//	    DSN: os.Getenv("MYSQL_DSN"),
//	    SQL: "SELECT * FROM orders WHERE id > ? ORDER BY id LIMIT ?",
//	    Args: []any{lastID, 50000},
//	}
//
// The driver appends `parseTime=true` to the DSN if you forget it. The result
// is the same without it -- there is a path for the raw text -- but with it the
// instant does not have to be reparsed in Go once per row.
type Query struct {
	// DSN is the connection string, in the driver's format:
	//
	//	user:password@tcp(host:3306)/database
	//
	// It carries a password, so it never appears in a log nor in Describe().
	DSN string

	// SQL is the query. The parameters are `?`, and not $1.
	//
	// Paginate by KEY and not by OFFSET: OFFSET on a large table is O(n^2),
	// because the server counts the discarded rows on every page.
	SQL string

	// Args are the parameters for `?`. Optional.
	Args []any

	// Timeout bounds the whole query. Zero means no bound.
	Timeout time.Duration

	// DB reuses a pool you already have. Nil opens one and closes it at the
	// end.
	DB *sql.DB
}

// Describe satisfies core.Reader. It says the query, never the DSN.
func (q Query) Describe() string { return "mysql: " + summarize(q.SQL) }

// Read satisfies core.Reader.
func (q Query) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	if q.DSN == "" && q.DB == nil {
		return nil, fmt.Errorf("mysql.Query needs DSN (or DB)")
	}
	if q.SQL == "" {
		return nil, fmt.Errorf("mysql.Query needs SQL")
	}

	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	db, closeDB, err := q.open()
	if err != nil {
		return nil, err
	}

	// The query is fired here, and not inside the iterator: a wrong DSN or a
	// missing table come back as an Extract error, and not as the first item of
	// a sequence the caller has to drain.
	rows, err := db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		closeDB()
		return nil, fmt.Errorf("mysql: %w", hideDSN(err, q.DSN))
	}

	types, err := rows.ColumnTypes()
	if err != nil {
		_ = rows.Close()
		closeDB()
		return nil, fmt.Errorf("mysql: reading column types: %w", err)
	}

	start := time.Now()
	return func(yield func(core.Envelope, error) bool) {
		defer closeDB()
		defer func() { _ = rows.Close() }()

		names := make([]string, len(types))
		declared := make([]string, len(types))
		for i, t := range types {
			names[i], declared[i] = t.Name(), strings.ToUpper(t.DatabaseTypeName())
		}

		// The destinations are reused across rows: one Scan per row allocating
		// N pointers and N values would double the cost of every record, and
		// Scan copies into the targets before returning.
		targets := make([]any, len(types))
		cells := make([]any, len(types))
		for i := range targets {
			targets[i] = &cells[i]
		}

		count := 0
		var sample []any

		defer func() {
			if opt.Stats != nil {
				opt.Stats.Pages, opt.Stats.Attempts = 1, 1
			}
			elapsed := time.Since(start)
			core.LogExtract(ctx, "mysql", q.Describe(), core.PreviewStats{
				Rows: count, Pages: 1, Duration: elapsed,
			})
			if opt.Preview > 0 {
				core.WritePreview(opt.PreviewWriter, sample, opt.PreviewBytes, core.PreviewStats{
					Rows: count, Pages: 1, Duration: elapsed,
				})
			}
		}()

		for rows.Next() {
			if err := rows.Scan(targets...); err != nil {
				yield(core.Envelope{}, fmt.Errorf("mysql: reading row %d: %w", count+1, err))
				return
			}

			record := make(map[string]any, len(names))
			for i, name := range names {
				record[name] = ToJSON(cells[i], declared[i])
			}

			count++
			if opt.Preview > 0 && len(sample) < opt.Preview {
				sample = append(sample, record)
			}
			if !yield(core.Envelope{Payload: record}, nil) {
				return
			}
		}

		if err := rows.Err(); err != nil {
			yield(core.Envelope{}, fmt.Errorf("mysql: after %d row(s): %w", count, err))
		}
	}, nil
}

func (q Query) open() (*sql.DB, func(), error) {
	if q.DB != nil {
		return q.DB, func() {}, nil
	}
	db, err := sql.Open("mysql", WithParseTime(q.DSN))
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: DSN is not valid")
	}
	return db, func() { _ = db.Close() }, nil
}

// WithParseTime makes sure parseTime=true is on the DSN.
//
// Without it the driver returns DATETIME and TIMESTAMP as []byte. ToJSON has a
// path for that and produces the same RFC 3339 -- so the result does not
// change, and there is a test proving it does not.
//
// What changes is the COST: without parseTime every instant is reparsed from
// text in Go, once per row, after the driver already did the work. On a load of
// hundreds of thousands of rows that is one allocation and one parse per
// record, for free.
//
// Appending beats refusing: whoever forgot has no way to know this was it, and
// the result would be identical either way.
func WithParseTime(dsn string) string {
	if strings.Contains(dsn, "parseTime=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&parseTime=true"
	}
	return dsn + "?parseTime=true"
}

// ComParseTime is the former name of WithParseTime.
//
// Deprecated: use WithParseTime. It is kept because it shipped in a published
// version. It will go in v1.
func ComParseTime(dsn string) string { return WithParseTime(dsn) }

func summarize(sql string) string {
	s := strings.TrimSpace(sql)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

func hideDSN(err error, dsn string) error {
	if err == nil || dsn == "" || !strings.Contains(err.Error(), dsn) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}
