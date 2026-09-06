// Package postgres writes records into PostgreSQL.
//
// It imports pgx. A fetcher that loads into a file or into BigQuery never
// compiles it -- the same measured rule that took BigQuery out of `to`.
package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Table loads records into a Postgres table.
//
//	To: postgres.Table{DSN: os.Getenv("PG_DSN"), Name: "landing.orders"}
//
// The table has to EXIST. This driver does not create it and infers no types:
// deducing NUMERIC(18,2) from an encoding/json float64 would be guessing, and
// guessing a type is the one thing this SDK decided not to do. The error says
// so, and lists the columns the batch carries, so the DDL comes out of one
// reading.
type Table struct {
	// DSN is the connection string. Required, and never appears in a log.
	DSN string

	// Name is the table, with its schema: "landing.orders". Required.
	Name string

	// Conn reuses a connection. Nil opens one and closes it at the end.
	Conn *pgx.Conn
}

// Describe satisfies core.Writer. It names the table, never the DSN.
func (t Table) Describe() string { return "postgres:" + t.Name }

// Write satisfies core.Writer.
func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	res := &core.LoadResult{Dedup: opt.Dedup, Strategy: "copy"}
	if opt.Dedup == "" {
		res.Dedup = core.DedupNone
	}
	start := time.Now()
	fail := func(err error) (*core.LoadResult, error) {
		res.Duration = time.Since(start)
		return res, err
	}

	if t.DSN == "" && t.Conn == nil {
		return fail(fmt.Errorf("postgres.Table needs DSN (or Conn)"))
	}
	if t.Name == "" {
		return fail(fmt.Errorf("postgres.Table needs Name, with the schema: \"landing.pedidos\""))
	}
	if len(envelopes) == 0 {
		return fail(nil)
	}

	// The record is exactly what the Transform chain composed, and the
	// declaration is checked against the whole of it -- ingestion_id included.
	if err := core.CheckColumns(opt.Columns, envelopes); err != nil {
		return fail(err)
	}

	conn, closeConn, err := t.connect(ctx)
	if err != nil {
		return fail(err)
	}
	defer closeConn()

	schema, table, err := splitName(t.Name)
	if err != nil {
		return fail(err)
	}

	tableColumns, types, err := columnsOf(ctx, conn, schema, table)
	if err != nil {
		return fail(err)
	}
	if len(tableColumns) == 0 {
		return fail(fmt.Errorf("table %s does not exist. This driver does not create it and "+
			"does not infer types -- guessing NUMERIC(18,2) from a JSON number is the one "+
			"thing this SDK will not do. Create it with the columns the batch carries: %s",
			t.Name, strings.Join(fieldsOf(envelopes), ", ")))
	}

	// What Reconcile buys HERE is not the positional match: pgx's CopyFrom
	// sends the column list along, so a value and a column do not drift apart
	// the way they drifted in BigQuery's INSERT ROW.
	//
	// What it buys is the refusal BEFORE touching the server, with the message
	// that fixes it: a field the table does not have would make Postgres return
	// `column "x" of relation "y" does not exist` in the middle of a COPY --
	// after the whole extract, and without saying what to do. And the table's
	// order keeps the column list stable across runs, instead of depending on
	// the order some map happened to be walked in.
	columns, err := core.Reconcile(tableColumns, fieldsOf(envelopes), t.Name)
	if err != nil {
		return fail(err)
	}

	if res.Dedup == core.DedupMerge {
		if err := checkUniqueIndex(ctx, conn, schema, table); err != nil {
			return fail(err)
		}
		rows, ignored, err := t.loadWithDedup(ctx, conn, columns, types, envelopes)
		res.RowsLoaded, res.RowsIgnored = rows, ignored
		return fail(err)
	}

	rows, err := t.copyRows(ctx, conn, columns, types, envelopes)
	res.RowsLoaded = rows
	return fail(err)
}

// copyRows uses COPY FROM STDIN, which is Postgres's fast path.
//
// pgx pulls rows out of the CopyFromSource on demand, so the batch is not
// materialized into a second structure: what lives in memory is the slice of
// envelopes that already arrived, plus one row at a time.
func (t Table) copyRows(ctx context.Context, conn *pgx.Conn, columns []string, types map[string]string, envelopes []core.Envelope) (int64, error) {
	src := &rows{columns: columns, types: types, envelopes: envelopes}
	n, err := conn.CopyFrom(ctx, pgx.Identifier(strings.Split(t.Name, ".")), columns, src)
	if err != nil {
		if src.err != nil {
			return 0, src.err
		}
		return 0, fmt.Errorf("postgres: COPY into %s: %w", t.Name, err)
	}
	return n, nil
}

// rows feeds CopyFrom one row at a time.
//
// It implements pgx.CopyFromSource rather than building a [][]any up front: on
// a batch of 500 thousand records the difference is between a slice of
// pointers and half a gigabyte of values alive at once. The value buffer is
// reused across rows, because pgx consumes each one before asking for the
// next.
type rows struct {
	columns   []string
	types     map[string]string
	envelopes []core.Envelope
	i         int
	buf       []any
	err       error
}

func (l *rows) Next() bool { return l.i < len(l.envelopes) && l.err == nil }

func (l *rows) Values() ([]any, error) {
	obj, err := core.AsObject(l.envelopes[l.i].Payload)
	if err != nil {
		l.err = fmt.Errorf("postgres: row %d: %w", l.i+1, err)
		return nil, l.err
	}
	if l.buf == nil {
		l.buf = make([]any, len(l.columns))
	}
	for j, c := range l.columns {
		// Absent becomes nil, which COPY writes as NULL.
		v, err := toColumn(obj[c], l.types[c])
		if err != nil {
			l.err = fmt.Errorf("postgres: row %d, column %q: %w", l.i+1, c, err)
			return nil, l.err
		}
		l.buf[j] = v
	}
	l.i++
	return l.buf, nil
}

func (l *rows) Err() error { return l.err }

// loadWithDedup goes through a temporary table and an ON CONFLICT DO NOTHING,
// which is the cheap equivalent of BigQuery's MERGE.
//
// The temporary table belongs to the SESSION and disappears on its own; there
// is no cleanup to forget.
func (t Table) loadWithDedup(ctx context.Context, conn *pgx.Conn, columns []string, types map[string]string, envelopes []core.Envelope) (int64, int64, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after the commit

	const tmp = "brevis_stage"
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`CREATE TEMP TABLE %s (LIKE %s INCLUDING DEFAULTS) ON COMMIT DROP`, tmp, t.Name)); err != nil {
		return 0, 0, fmt.Errorf("postgres: staging table: %w", err)
	}

	src := &rows{columns: columns, types: types, envelopes: envelopes}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{tmp}, columns, src); err != nil {
		if src.err != nil {
			return 0, 0, src.err
		}
		return 0, 0, fmt.Errorf("postgres: COPY into staging: %w", err)
	}

	tag, err := tx.Exec(ctx, InsertSQL(t.Name, tmp, columns))
	if err != nil {
		return 0, 0, fmt.Errorf("postgres: insert into %s: %w", t.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("postgres: commit: %w", err)
	}

	inserted := tag.RowsAffected()
	return inserted, int64(len(envelopes)) - inserted, nil
}

// InsertSQL builds the dedup's INSERT ... ON CONFLICT.
//
// Exported and pure because SQL built inside a method that holds a client was
// never seen by a test -- that is how BigQuery's MERGE shipped with a
// positional match and cost v0.12.0. The columns are NAMED, always.
func InsertSQL(target, source string, columns []string) string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = quote(c)
	}
	list := strings.Join(names, ", ")
	return fmt.Sprintf(
		"INSERT INTO %s (%s) SELECT %s FROM %s ON CONFLICT (%s) DO NOTHING",
		target, list, list, source, quote(core.MetadataID))
}

// quote wraps the identifier in quotes. A column called "order" or "select" is
// legitimate, and unquoted it becomes a syntax error in the middle of a load.
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func splitName(name string) (schema, table string, err error) {
	parts := strings.Split(name, ".")
	switch len(parts) {
	case 1:
		return "public", parts[0], nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("postgres.Table.Name %q has too many parts; use \"schema.table\"", name)
	}
}

// columnsOf reads the real schema: the names in the order the table declares
// them, and the type of each one.
//
// The ORDER matters because COPY matches by position. The TYPES matter because
// the SDK's record is JSON, and a timestamp in it is a string -- see toColumn.
func columnsOf(ctx context.Context, conn *pgx.Conn, schema, table string) ([]string, map[string]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2
		 ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: reading the schema of %s.%s: %w", schema, table, err)
	}
	defer rows.Close()

	var out []string
	types := map[string]string{}
	for rows.Next() {
		var c, typ string
		if err := rows.Scan(&c, &typ); err != nil {
			return nil, nil, err
		}
		out = append(out, c)
		types[c] = typ
	}
	return out, types, rows.Err()
}

// checkUniqueIndex requires the index, and does not create it.
//
// A loader that can create an index can lock a production table in the middle
// of the working day. The refusal names the missing index and shows the
// command.
func checkUniqueIndex(ctx context.Context, conn *pgx.Conn, schema, table string) error {
	var exists bool
	err := conn.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_index i
		   JOIN pg_class c   ON c.oid = i.indrelid
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		   WHERE n.nspname = $1 AND c.relname = $2
		     AND i.indisunique AND i.indnatts = 1 AND a.attname = $3)`,
		schema, table, core.MetadataID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("postgres: checking the unique index: %w", err)
	}
	if !exists {
		return fmt.Errorf("dedup needs a unique index on %s, and %s.%s does not have one -- "+
			"without it ON CONFLICT has nothing to match and every run would insert duplicates. "+
			"This driver does not create indexes, because a loader that can create one can lock "+
			"a production table: CREATE UNIQUE INDEX CONCURRENTLY ON %s.%s (%s)",
			core.MetadataID, schema, table, schema, table, core.MetadataID)
	}
	return nil
}

// fieldsOf returns the sorted union of the batch's fields.
func fieldsOf(envelopes []core.Envelope) []string {
	seen := map[string]bool{}
	for _, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			continue
		}
		for k := range obj {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (t Table) connect(ctx context.Context) (*pgx.Conn, func(), error) {
	if t.Conn != nil {
		return t.Conn, func() {}, nil
	}
	cfg, err := pgx.ParseConfig(t.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: DSN is not valid")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: connecting: %w", hideDSN(err, t.DSN))
	}
	return conn, func() { _ = conn.Close(context.WithoutCancel(ctx)) }, nil
}

func hideDSN(err error, dsn string) error {
	if err == nil || dsn == "" || !strings.Contains(err.Error(), dsn) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}

// CheckDestination satisfies core.DestinationChecker: it checks the
// declaration against the real table, before the extraction happens.
//
// On a vendor with a quota, the difference between checking here and checking
// in Write is the difference between one information_schema query and the whole
// quota window spent to find out that a column does not match.
//
// A table that does not exist is NOT an error here, but in Write -- which is
// where the message also lists the batch's columns, so the DDL comes out of one
// reading.
func (t Table) CheckDestination(ctx context.Context, columns []string) error {
	if len(columns) == 0 || (t.DSN == "" && t.Conn == nil) || t.Name == "" {
		return nil
	}

	conn, closeConn, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConn()

	schema, table, err := splitName(t.Name)
	if err != nil {
		return err
	}
	ofTable, _, err := columnsOf(ctx, conn, schema, table)
	if err != nil || len(ofTable) == 0 {
		return err
	}

	has := make(map[string]bool, len(ofTable))
	for _, c := range ofTable {
		has[c] = true
	}
	var missing []string
	for _, c := range columns {
		if !has[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("the declaration lists %s, which %s does not have. The table has: %s. "+
		"Caught before the extract, so no source quota was spent",
		strings.Join(missing, ", "), t.Name, strings.Join(ofTable, ", "))
}
