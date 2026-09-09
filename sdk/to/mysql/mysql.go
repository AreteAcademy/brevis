// Package mysql writes records into MySQL.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" driver

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Table loads records into a MySQL table.
//
//	To: mysql.Table{DSN: os.Getenv("MYSQL_DSN"), Name: "landing.orders"}
//
// The table has to exist, and there are three ways to get one: create it
// yourself, declare Target.Schema with CreateTable on, or give CreateSQL.
//
// What it will NOT do is infer. Deducing DECIMAL(18,2) from a JSON number is
// guessing, and guessing a type is the one thing this SDK decided not to do.
type Table struct {
	// DSN is the connection string. Required, and never appears in a log.
	DSN string

	// Name is the table, with the database when it is not the DSN's.
	Name string

	// BatchSize is how many rows go per INSERT. Zero uses 1000.
	//
	// This field exists here and does NOT exist on the Postgres driver, and the
	// difference is real: Postgres has COPY FROM STDIN, which streams
	// everything. MySQL has no reliable equivalent -- LOAD DATA LOCAL INFILE
	// usually ships disabled on both server and client -- so the load is a
	// multi-row INSERT, and the batch size is a choice for whoever loads:
	// large packets run into max_allowed_packet.
	BatchSize int

	// DB reuses a pool. Nil opens one and closes it at the end.
	DB *sql.DB

	// CreateTable lets the driver create the table when it is absent, from
	// Target.Schema or from CreateSQL. Off by default: a loader that creates
	// tables by accident turns a typo in Name into a second table nobody reads.
	CreateTable bool

	// CreateSQL is your DDL, run once when the table is absent and only with
	// CreateTable on. It is for what a declaration cannot say: DECIMAL with a
	// scale, an engine, a charset, an index.
	CreateSQL string

	// Evolve says what the load may do to a table that EXISTS and no longer
	// matches Target.Schema. The zero value refuses any difference, which is
	// what this driver has always done. See core.Evolution.
	Evolve core.Evolution
}

const defaultBatch = 1000

// Describe satisfies core.Writer. It names the table, never the DSN.
func (t Table) Describe() string { return "mysql:" + t.Name }

// Write satisfies core.Writer.
func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	res := &core.LoadResult{Dedup: opt.Dedup, Strategy: "insert"}
	if opt.Dedup == "" {
		res.Dedup = core.DedupNone
	}
	start := time.Now()
	fail := func(err error) (*core.LoadResult, error) {
		res.Duration = time.Since(start)
		return res, err
	}

	if t.DSN == "" && t.DB == nil {
		return fail(fmt.Errorf("mysql.Table needs DSN (or DB)"))
	}
	if t.Name == "" {
		return fail(fmt.Errorf("mysql.Table needs Name"))
	}
	if len(envelopes) == 0 {
		return fail(nil)
	}
	if err := core.CheckRow(opt.Columns, opt.Schema, envelopes); err != nil {
		return fail(err)
	}

	db, closeDB, err := t.open()
	if err != nil {
		return fail(err)
	}
	defer closeDB()

	database, table := splitName(t.Name)
	tableColumns, types, err := columnsOf(ctx, db, database, table)
	if err != nil {
		return fail(err)
	}
	if len(tableColumns) == 0 {
		if err := t.create(ctx, db, opt, envelopes); err != nil {
			return fail(err)
		}
		// Read it back rather than trusting the CREATE: the column list and the
		// types below drive the INSERT, and a CreateSQL that produced something
		// else would fail later with an error about a column instead of about
		// the DDL that forgot it.
		tableColumns, types, err = columnsOf(ctx, db, database, table)
		if err != nil {
			return fail(err)
		}
		if len(tableColumns) == 0 {
			return fail(fmt.Errorf("the table %s still does not exist after creating it", t.Name))
		}
		res.TableCreated = true
	} else if err := t.evolve(ctx, db, opt, types); err != nil {
		return fail(err)
	} else if len(opt.Schema) > 0 {
		tableColumns, types, err = columnsOf(ctx, db, database, table)
		if err != nil {
			return fail(err)
		}
	}

	columns, err := core.Reconcile(tableColumns, fieldsOf(envelopes), t.Name)
	if err != nil {
		return fail(err)
	}

	if res.Dedup == core.DedupMerge {
		if err := checkUniqueIndex(ctx, db, database, table); err != nil {
			return fail(err)
		}
	}

	count, err := t.insert(ctx, db, columns, types, envelopes, res.Dedup == core.DedupMerge)
	res.RowsLoaded = count
	res.RowsIgnored = int64(len(envelopes)) - count
	if res.Dedup != core.DedupMerge {
		res.RowsIgnored = 0
	}
	return fail(err)
}

// insert sends multi-row INSERTs in batches, inside a transaction.
//
// The SQL is built once per batch and reused; the arguments go in a reused
// slice. Building the string per row would cost one concatenation per record,
// which on a load of hundreds of thousands is the dominant cost.
func (t Table) insert(ctx context.Context, db *sql.DB, columns []string, types map[string]string, envelopes []core.Envelope, ignore bool) (int64, error) {
	size := t.BatchSize
	if size <= 0 {
		size = defaultBatch
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mysql: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op after the commit

	var total int64
	args := make([]any, 0, size*len(columns))

	for start := 0; start < len(envelopes); start += size {
		end := min(start+size, len(envelopes))
		block := envelopes[start:end]

		args = args[:0]
		for i, e := range block {
			obj, err := core.AsObject(e.Payload)
			if err != nil {
				return total, fmt.Errorf("mysql: row %d: %w", start+i+1, err)
			}
			for _, c := range columns {
				v, err := toColumn(obj[c], types[c])
				if err != nil {
					return total, fmt.Errorf("mysql: row %d, column %q: %w", start+i+1, c, err)
				}
				args = append(args, v)
			}
		}

		tag, err := tx.ExecContext(ctx, InsertSQL(t.Name, columns, len(block), ignore), args...)
		if err != nil {
			return total, fmt.Errorf("mysql: insert into %s: %w", t.Name, err)
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("mysql: rows affected: %w", err)
		}
		total += n
	}

	if err := tx.Commit(); err != nil {
		return total, fmt.Errorf("mysql: commit: %w", err)
	}
	return total, nil
}

// InsertSQL builds the multi-row INSERT.
//
// Exported and pure because SQL built inside a method that holds a client was
// never seen by a test -- that is how BigQuery's MERGE shipped with a
// positional match and cost v0.12.0. The columns are NAMED, always.
//
// `ignore` becomes INSERT IGNORE, which is MySQL's dedup: with a unique index
// on ingestion_id, a repeated row is discarded rather than taking the batch
// down.
func InsertSQL(table string, columns []string, rows int, ignore bool) string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = quote(c)
	}

	oneRow := "(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
	values := make([]string, rows)
	for i := range values {
		values[i] = oneRow
	}

	verb := "INSERT"
	if ignore {
		verb = "INSERT IGNORE"
	}
	return fmt.Sprintf("%s INTO %s (%s) VALUES %s",
		verb, qualify(table), strings.Join(names, ", "), strings.Join(values, ", "))
}

// quote uses a backtick, which is MySQL's delimiter. A column called `order` is
// legitimate, and unquoted it becomes a syntax error in the middle of a load.
func quote(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// qualify quotes each part of "database.table" separately: quoting the whole
// name would create a table called "database.table".
func qualify(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = quote(p)
	}
	return strings.Join(parts, ".")
}

func splitName(name string) (database, table string) {
	if i := strings.Index(name, "."); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// columnsOf reads the real schema: the names in declared order and the type of
// each one.
func columnsOf(ctx context.Context, db *sql.DB, database, table string) ([]string, map[string]string, error) {
	// DATABASE() covers the case of the database coming from the DSN rather
	// than from Name.
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE()) AND table_name = ?
		 ORDER BY ordinal_position`, database, table)
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: reading the schema of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	types := map[string]string{}
	for rows.Next() {
		var c, typ string
		if err := rows.Scan(&c, &typ); err != nil {
			return nil, nil, err
		}
		out = append(out, c)
		types[c] = strings.ToLower(typ)
	}
	return out, types, rows.Err()
}

// checkUniqueIndex requires the index, and does not create it.
func checkUniqueIndex(ctx context.Context, db *sql.DB, database, table string) error {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.statistics s
		 WHERE s.table_schema = COALESCE(NULLIF(?, ''), DATABASE())
		   AND s.table_name = ? AND s.non_unique = 0 AND s.column_name = ?
		   AND s.seq_in_index = 1
		   AND (SELECT COUNT(*) FROM information_schema.statistics x
		        WHERE x.table_schema = s.table_schema AND x.table_name = s.table_name
		          AND x.index_name = s.index_name) = 1`,
		database, table, core.MetadataID).Scan(&n)
	if err != nil {
		return fmt.Errorf("mysql: checking the unique index: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("dedup needs a unique index on %s, and %s does not have one -- "+
			"without it INSERT IGNORE has nothing to match and every run would insert "+
			"duplicates. This driver does not create indexes, because a loader that can "+
			"create one can lock a production table: "+
			"CREATE UNIQUE INDEX idx_%s ON %s (%s)",
			core.MetadataID, table, core.MetadataID, table, core.MetadataID)
	}
	return nil
}

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

func (t Table) open() (*sql.DB, func(), error) {
	if t.DB != nil {
		return t.DB, func() {}, nil
	}
	db, err := sql.Open("mysql", comParseTime(t.DSN))
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: DSN is not valid")
	}
	return db, func() { _ = db.Close() }, nil
}

func comParseTime(dsn string) string {
	if strings.Contains(dsn, "parseTime=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&parseTime=true"
	}
	return dsn + "?parseTime=true"
}

// CheckDestination satisfaz core.DestinationChecker. Mesmo motivo do Postgres:
// checking early costs one information_schema query; checking in Write costs
// the vendor's whole quota window.
func (t Table) CheckDestination(ctx context.Context, columns []string) error {
	if len(columns) == 0 || (t.DSN == "" && t.DB == nil) || t.Name == "" {
		return nil
	}

	db, closeDB, err := t.open()
	if err != nil {
		return err
	}
	defer closeDB()

	database, table := splitName(t.Name)
	inTable, _, err := columnsOf(ctx, db, database, table)
	if err != nil || len(inTable) == 0 {
		return err
	}

	has := make(map[string]bool, len(inTable))
	for _, c := range inTable {
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
		strings.Join(missing, ", "), t.Name, strings.Join(inTable, ", "))
}

// create makes the table, from the declaration or from the caller's DDL.
//
// Only when it is ABSENT -- never to alter one that exists. A loader that can
// ALTER or DROP is a loader that can erase history.
func (t Table) create(ctx context.Context, db *sql.DB, opt core.WriteOptions, envelopes []core.Envelope) error {
	if !t.CreateTable {
		return fmt.Errorf("table %s does not exist. Set CreateTable and declare "+
			"Target.Schema (or CreateSQL) to let the driver create it, or create it "+
			"yourself with the columns the batch carries: %s",
			t.Name, strings.Join(fieldsOf(envelopes), ", "))
	}

	if t.CreateSQL != "" {
		if _, err := db.ExecContext(ctx, t.CreateSQL); err != nil {
			return fmt.Errorf("running CreateSQL for %s: %w", t.Name, err)
		}
		return nil
	}

	if len(opt.Schema) == 0 {
		return fmt.Errorf("table %s does not exist and CreateTable is on, but nothing "+
			"says what to create: declare Target.Schema with a type on each column, or "+
			"give CreateSQL. This driver does not infer types from the batch", t.Name)
	}

	ddl, err := opt.Schema.CreateTable(core.MySQL, t.Name)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("creating %s: %w\n%s", t.Name, err, ddl)
	}
	return nil
}

// evolve brings an EXISTING table up to the declaration, within what Evolve
// allows. See the Postgres driver's twin for the reasoning; the difference here
// is only the dialect.
func (t Table) evolve(ctx context.Context, db *sql.DB, opt core.WriteOptions, types map[string]string) error {
	if len(opt.Schema) == 0 {
		return nil
	}

	changes, err := opt.Schema.Plan(declaredTypes(types), t.Evolve, t.Name)
	if err != nil {
		return err
	}

	for done, ch := range changes {
		stmts, err := opt.Schema.AlterTable(core.MySQL, t.Name, []core.Change{ch})
		if err != nil {
			return err
		}
		for _, sql := range stmts {
			if _, err := db.ExecContext(ctx, sql); err != nil {
				return fmt.Errorf("evolving %s (%s): %w\n%d of %d changes had already been applied",
					t.Name, ch, err, done, len(changes))
			}
		}
		slog.InfoContext(ctx, "schema evolved", "table", t.Name, "change", ch.String())
	}
	return nil
}
