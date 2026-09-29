package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"os"
	"strings"
	"testing"
	"time"

	frommy "github.com/AreteAcademy/brevis/sdk/from/mysql"
	tomy "github.com/AreteAcademy/brevis/sdk/to/mysql"

	"github.com/AreteAcademy/brevis/sdk"
)

// docker compose -f docker-compose.drivers.yml up -d mysql
// BREVIS_IT_MYSQL_DSN='root:brevis@tcp(localhost:53306)/brevis_it' go test ./sdk/to/mysql/
func dsn(t *testing.T) string {
	t.Helper()
	d := os.Getenv("BREVIS_IT_MYSQL_DSN")
	if d == "" {
		t.Skip("BREVIS_IT_MYSQL_DSN não definida; suba o docker-compose.drivers.yml")
	}
	return d
}

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", frommy.WithParseTime(dsn(t)))
	if err != nil {
		t.Fatalf("abrindo: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func table(t *testing.T, db *sql.DB, ddl string) string {
	t.Helper()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	if _, err := db.Exec(fmt.Sprintf("CREATE TABLE %s (%s)", name, ddl)); err != nil {
		t.Fatalf("criando %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + name) })
	return name
}

const defaultColumns = "" +
	"ingestion_id VARCHAR(36) NOT NULL," +
	"ingestion_loaded_at DATETIME(6) NOT NULL," +
	"provider VARCHAR(64)," +
	"source_key VARCHAR(64)," +
	"valor DECIMAL(18,2)"

func lote(n int) []sdk.Envelope {
	out := make([]sdk.Envelope, n)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range out {
		out[i] = sdk.Envelope{Payload: map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%04d", i),
			"ingestion_loaded_at": now,
			"provider":            "teste",
			"source_key":          fmt.Sprintf("k%d", i),
			"valor":               "10.50",
		}}
	}
	return out
}

// TestIntegrationARowActuallyGoesIn: the in-memory tests prove the bytes
// we assembled, not what the server accepts.
func TestIntegrationARowActuallyGoesIn(t *testing.T) {
	db := open(t)
	name := table(t, db, defaultColumns)

	res, err := tomy.Table{DSN: dsn(t), Name: name}.Write(
		context.Background(), lote(3), sdk.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.RowsLoaded != 3 {
		t.Errorf("RowsLoaded = %d, esperado 3", res.RowsLoaded)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("o servidor tem %d linhas", n)
	}
}

// TestIntegrationTheTablesOrder: every value has to land in the right column
// when the table's order is not the record's.
func TestIntegrationTheTablesOrder(t *testing.T) {
	db := open(t)
	name := table(t, db, "valor DECIMAL(18,2), provider VARCHAR(64), "+
		"ingestion_loaded_at DATETIME(6) NOT NULL, ingestion_id VARCHAR(36) NOT NULL")

	l := []sdk.Envelope{{Payload: map[string]any{
		"ingestion_id":        "abc",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"provider":            "acme",
		"valor":               "99.90",
	}}}
	if _, err := (tomy.Table{DSN: dsn(t), Name: name}).Write(
		context.Background(), l, sdk.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var id, prov, value string
	if err := db.QueryRow("SELECT ingestion_id, provider, valor FROM "+name).
		Scan(&id, &prov, &value); err != nil {
		t.Fatal(err)
	}
	if id != "abc" || prov != "acme" || value != "99.90" {
		t.Errorf("valores trocados: id=%q provider=%q valor=%q", id, prov, value)
	}
}

// TestIntegrationDedupLoadsTheSameBatchTwice is the done criterion of
// phase 3: the same pipeline as phase 2, with one row swapped.
func TestIntegrationDedupLoadsTheSameBatchTwice(t *testing.T) {
	db := open(t)
	name := table(t, db, defaultColumns)
	if _, err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX u ON %s (ingestion_id)", name)); err != nil {
		t.Fatal(err)
	}

	target := tomy.Table{DSN: dsn(t), Name: name}
	l := lote(5)
	opt := sdk.WriteOptions{Dedup: sdk.DedupMerge}

	first, err := target.Write(context.Background(), l, opt)
	if err != nil {
		t.Fatalf("primeira: %v", err)
	}
	if first.RowsLoaded != 5 {
		t.Errorf("primeira: %d carregadas", first.RowsLoaded)
	}

	segunda, err := target.Write(context.Background(), l, opt)
	if err != nil {
		t.Fatalf("segunda: %v", err)
	}
	if segunda.RowsLoaded != 0 || segunda.RowsIgnored != 5 {
		t.Errorf("segunda: %d carregadas, %d ignoradas -- esperava 0 e 5",
			segunda.RowsLoaded, segunda.RowsIgnored)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("a tabela tem %d linhas depois de duas cargas do mesmo lote", n)
	}
}

// TestIntegrationDedupWithoutAnIndexRefuses: with no unique index, INSERT IGNORE
// has
// nothing to match and every run would insert duplicates.
func TestIntegrationDedupWithoutAnIndexRefuses(t *testing.T) {
	db := open(t)
	name := table(t, db, defaultColumns)

	_, err := tomy.Table{DSN: dsn(t), Name: name}.Write(
		context.Background(), lote(1), sdk.WriteOptions{Dedup: sdk.DedupMerge})
	if err == nil {
		t.Fatal("dedup sem índice único passou")
	}
	if !strings.Contains(err.Error(), "CREATE UNIQUE INDEX") {
		t.Errorf("o erro não diz o comando: %v", err)
	}
}

// TestIntegrationAFieldTheTableLacksIsRefused: refusing BEFORE the server, with
// the
// way out written down.
func TestIntegrationAFieldTheTableLacksIsRefused(t *testing.T) {
	db := open(t)
	name := table(t, db, defaultColumns)

	l := []sdk.Envelope{{Payload: map[string]any{
		"ingestion_id":        "x",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"coluna_inexistente":  1,
	}}}
	_, err := tomy.Table{DSN: dsn(t), Name: name}.Write(context.Background(), l, sdk.WriteOptions{})
	if err == nil {
		t.Fatal("campo sem coluna passou")
	}
	for _, exigido := range []string{"coluna_inexistente", "remove the field in Transform"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro não diz %q: %v", exigido, err)
		}
	}
}

// TestIntegrationTheReadIsStreamed falha se o driver bufferizar.
func TestIntegrationTheReadIsStreamed(t *testing.T) {
	db := open(t)
	name := table(t, db, "i INT, texto TEXT")
	// 20 thousand rows through recursion: MySQL has no generate_series, and the
	// default
	// recursion limit is 1000.
	//
	// The connection is pinned: a SET SESSION on a *sql.DB applies to the
	// connection the
	// pool happened to pick, and the next INSERT may go out on another -- a test
	// that
	// passes by luck is worse than a slow test.
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(context.Background(),
		"SET SESSION cte_max_recursion_depth = 50000"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), fmt.Sprintf(`INSERT INTO %s
		WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 20000)
		SELECT n, REPEAT('x', 500) FROM s`, name)); err != nil {
		t.Fatal(err)
	}

	seq, err := frommy.Query{DSN: dsn(t), SQL: "SELECT i, texto FROM " + name + " ORDER BY i"}.
		Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 20000 {
		t.Fatalf("a tabela tem %d linhas; o teste precisa das 20 mil para significar algo", n)
	}

	start := time.Now()
	recebeu := false
	for _, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		recebeu = true
		break
	}
	if !recebeu {
		t.Fatal("nenhuma linha")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a primeira linha levou %s; o driver parece bufferizar", d)
	}
}

// TestIntegrationTheTypesComeFromTheServer proves the type table against a real
// MySQL.
// database/sql returns []byte for nearly everything when read into an any,
// so without the declared type every DECIMAL would become base64 in the JSON.
func TestIntegrationTheTypesComeFromTheServer(t *testing.T) {
	db := open(t)
	name := table(t, db, `
		numerico DECIMAL(20,2),
		data DATE,
		instante DATETIME(6),
		documento JSON,
		bytes VARBINARY(16),
		inteiro BIGINT,
		texto VARCHAR(32),
		vazio VARCHAR(8)`)
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s VALUES
		('123456789012345678.99', '2026-09-05', '2026-09-05 12:30:00',
		 '{"a":[1,2]}', 0xDEADBEEF, 42, 'ola', NULL)`, name)); err != nil {
		t.Fatal(err)
	}

	seq, err := frommy.Query{DSN: dsn(t), SQL: "SELECT * FROM " + name}.
		Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var line map[string]any
	for e, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		line = e.Payload.(map[string]any)
	}
	if line == nil {
		t.Fatal("nenhuma linha")
	}

	expected := map[string]any{
		"numerico": "123456789012345678.99",
		"data":     "2026-09-05",
		"instante": "2026-09-05T12:30:00Z",
		"inteiro":  int64(42),
		"texto":    "ola",
		"vazio":    nil,
	}
	for field, quero := range expected {
		if got := line[field]; got != quero {
			t.Errorf("%s = %#v (%T), esperado %#v", field, got, got, quero)
		}
	}
	if _, ok := line["documento"].(map[string]any); !ok {
		t.Errorf("documento = %#v; JSON devia chegar aninhado", line["documento"])
	}
	if b, ok := line["bytes"].([]byte); !ok || len(b) != 4 {
		t.Errorf("bytes = %#v; VARBINARY devia chegar como []byte", line["bytes"])
	}
}

// TestIntegrationMySQLToMySQL is phase 3's done criterion: the same
// pipeline as phase 2, with one row swapped.
func TestIntegrationMySQLToMySQL(t *testing.T) {
	db := open(t)

	src := table(t, db, "id INT, nome VARCHAR(64), valor DECIMAL(18,2), atualizado_em DATETIME(6)")
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s
		WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 100)
		SELECT n, CONCAT('registro ', n), n * 1.5, NOW(6) FROM s`, src)); err != nil {
		t.Fatal(err)
	}

	target := table(t, db, `
		ingestion_id VARCHAR(36) NOT NULL,
		ingestion_loaded_at DATETIME(6) NOT NULL,
		provider VARCHAR(32) NOT NULL,
		entity VARCHAR(32) NOT NULL,
		source_key VARCHAR(32) NOT NULL,
		record_ts VARCHAR(64) NOT NULL,
		nome VARCHAR(64),
		valor DECIMAL(18,2)`)
	if _, err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX u ON %s (ingestion_id)", target)); err != nil {
		t.Fatal(err)
	}

	runIt := func() *sdk.Result {
		t.Helper()
		data, err := sdk.Extract(context.Background(), sdk.Source{
			From: frommy.Query{DSN: dsn(t),
				SQL: "SELECT id, nome, valor, atualizado_em FROM " + src + " ORDER BY id"},
		})
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		data = sdk.Transform(data,
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return fmt.Sprint(r["id"]), nil
			}),
			sdk.Without("id"),
			sdk.Rename(map[string]string{"atualizado_em": "record_ts"}),
			sdk.Compute("provider", func(map[string]any) (any, error) { return "mysql", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "registros", nil }),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		)
		res, err := sdk.Load(context.Background(), data, sdk.Target{
			To: tomy.Table{DSN: dsn(t), Name: target},
			Columns: []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity",
				"source_key", "record_ts", "nome", "valor"},
			Dedup: sdk.DedupMerge,
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return res
	}

	if first := runIt(); first.Rows != 100 {
		t.Errorf("primeira carga: %d linhas, esperado 100", first.Rows)
	}
	if segunda := runIt(); segunda.Rows != 0 || segunda.Ignored != 100 {
		t.Errorf("segunda carga: %d carregadas e %d ignoradas", segunda.Rows, segunda.Ignored)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 100 {
		t.Errorf("o destino tem %d linhas depois de duas execuções idênticas", n)
	}

	var value string
	if err := db.QueryRow("SELECT valor FROM " + target + " WHERE source_key = '2'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "3.00" {
		t.Errorf("valor = %q, esperado \"3.00\"", value)
	}
}

// TestIntegrationCreateTableFromTheDeclaration.
//
// MySQL's half of the same gate. The dialect makes three choices that only a
// real server can settle: LONGTEXT instead of a guessed VARCHAR length,
// TINYINT(1) for bool, and DEFAULT before NOT NULL — the other order is a
// syntax error here and parses fine everywhere else.
func TestIntegrationCreateTableFromTheDeclaration(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_created_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+name) })

	schema := sdk.Schema{
		{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
		{Name: "sku", Type: sdk.TypeString, Required: true},
		{Name: "quantity", Type: sdk.TypeInt64},
		{Name: "price", Type: sdk.TypeNumeric},
		{Name: "active", Type: sdk.TypeBool, Default: true},
		{Name: "status", Type: sdk.TypeString, Required: true, Default: "pending"},
		{Name: "attempts", Type: sdk.TypeInt64, Default: 0},
		{Name: "payload", Type: sdk.TypeJSON},
	}

	row := sdk.Envelope{Payload: map[string]any{
		"ingestion_id": "id-1", "ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"sku": "A1", "quantity": 3, "price": "9.90", "payload": map[string]any{"k": 1},
	}}
	res, err := tomy.Table{DSN: dsn(t), Name: name, CreateTable: true}.Write(
		ctx, []sdk.Envelope{row}, sdk.WriteOptions{Schema: schema, Columns: schema.Names()})
	if err != nil {
		t.Fatalf("the load failed: %v", err)
	}
	if !res.TableCreated {
		t.Error("the load did not report creating the table")
	}

	// What the server actually made. A LONGTEXT that came out VARCHAR(255)
	// would truncate silently on the first long value.
	rows, err := db.QueryContext(ctx, `
		SELECT column_name, column_type, is_nullable, IFNULL(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ?
		ORDER BY ordinal_position`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	got := map[string][3]string{}
	for rows.Next() {
		var col, typ, nullable, def string
		if err := rows.Scan(&col, &typ, &nullable, &def); err != nil {
			t.Fatal(err)
		}
		got[col] = [3]string{typ, nullable, def}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for col, want := range map[string][3]string{
		"sku":      {"longtext", "NO", ""},
		"quantity": {"bigint", "YES", ""},
		"price":    {"decimal(38,9)", "YES", ""},
		"active":   {"tinyint(1)", "YES", "1"},
		// An expression default reads back with a charset introducer --
		// `_utf8mb4'pending'` -- whose prefix depends on the CONNECTION's charset,
		// not on the DDL. Asserting it exactly would be testing MySQL's
		// formatting; `~` below means "contains", and the row inserted after this
		// is what proves the default actually fires.
		"status":   {"longtext", "NO", "~pending"},
		"attempts": {"bigint", "YES", "0"},
		"payload":  {"json", "YES", ""},
	} {
		g, ok := got[col]
		if !ok {
			t.Errorf("the table has no column %q", col)
			continue
		}
		if g[0] != want[0] {
			t.Errorf("%s is %s, wanted %s", col, g[0], want[0])
		}
		if g[1] != want[1] {
			t.Errorf("%s nullable=%s, wanted %s", col, g[1], want[1])
		}
		// A `~` prefix means "contains", for the one value whose exact text
		// belongs to the server.
		if strings.HasPrefix(want[2], "~") {
			if !strings.Contains(g[2], strings.TrimPrefix(want[2], "~")) {
				t.Errorf("%s default=%q, wanted something containing %q",
					col, g[2], strings.TrimPrefix(want[2], "~"))
			}
		} else if g[2] != want[2] {
			t.Errorf("%s default=%q, wanted %q", col, g[2], want[2])
		}
	}

	// And `status` proves the clause ORDER: it is NOT NULL with a DEFAULT, and
	// a row that omits it has to come back with the default rather than being
	// refused.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+name+" (ingestion_id, ingestion_loaded_at, sku) VALUES ('x', NOW(6), 'B2')"); err != nil {
		t.Fatalf("inserting a row that relies on the defaults: %v", err)
	}
	var status string
	var attempts int64
	if err := db.QueryRowContext(ctx,
		"SELECT status, attempts FROM "+name+" WHERE sku = 'B2'").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 0 {
		t.Errorf("the defaults did not apply: status=%q attempts=%d", status, attempts)
	}
}

// TestIntegrationEvolveAddsAColumnWithoutRewritingHistory.
//
// MySQL's half. The interesting part is the same one Postgres found: an
// `ADD COLUMN ... DEFAULT` backfills the rows already there, so the two
// statements have to stay apart. And MySQL needs the parenthesised form for a
// TEXT default even in SET DEFAULT, which only this test can settle.
func TestIntegrationEvolveAddsAColumnWithoutRewritingHistory(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_evolved_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+name) })

	base := sdk.Schema{
		{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
		{Name: "sku", Type: sdk.TypeString},
	}
	row := func(id, sku string, extra map[string]any) sdk.Envelope {
		p := map[string]any{
			"ingestion_id": id, "ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
			"sku": sku,
		}
		for k, v := range extra {
			p[k] = v
		}
		return sdk.Envelope{Payload: p}
	}

	writer := tomy.Table{DSN: dsn(t), Name: name, CreateTable: true}
	if _, err := writer.Write(ctx, []sdk.Envelope{row("a", "A1", nil)},
		sdk.WriteOptions{Schema: base, Columns: base.Names()}); err != nil {
		t.Fatalf("the first load failed: %v", err)
	}

	grown := append(append(sdk.Schema{}, base...),
		sdk.Column{Name: "channel", Type: sdk.TypeString, Default: "web"},
	)

	// Without Evolve it refuses, naming the way to allow it.
	refuser := tomy.Table{DSN: dsn(t), Name: name}
	if _, err := refuser.Write(ctx, []sdk.Envelope{row("b", "B2", nil)},
		sdk.WriteOptions{Schema: grown, Columns: grown.Names()}); err == nil {
		t.Fatal("a table missing a declared column was written to anyway")
	} else if !strings.Contains(err.Error(), "EvolveAdditive") {
		t.Errorf("the refusal does not say how to allow it: %v", err)
	}

	evolver := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive}
	if _, err := evolver.Write(ctx, []sdk.Envelope{row("b", "B2", nil)},
		sdk.WriteOptions{Schema: grown, Columns: grown.Names()}); err != nil {
		t.Fatalf("the evolving load failed: %v", err)
	}

	// The row that was already there is NULL in the new column, not "web".
	var channel *string
	if err := db.QueryRowContext(ctx,
		"SELECT channel FROM "+name+" WHERE ingestion_id = 'a'").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if channel != nil {
		t.Errorf("the existing row was backfilled with %q", *channel)
	}

	// And a row written afterwards, omitting it, gets the default -- which is
	// what proves SET DEFAULT took on a TEXT column.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+name+" (ingestion_id, ingestion_loaded_at, sku) VALUES ('c', NOW(6), 'C3')"); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := db.QueryRowContext(ctx,
		"SELECT channel FROM "+name+" WHERE ingestion_id = 'c'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != "web" {
		t.Errorf("the default did not apply to a new row: %q", after)
	}
}

// oneRow is a source of exactly one record, so a pipeline can run without a
// vendor behind it.
type oneRow struct{ payload map[string]any }

func (oneRow) Describe() string { return "one row" }
func (o oneRow) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		yield(sdk.Envelope{Provider: "p", Entity: "e", SourceKey: "k",
			RecordTS: "2026-09-29T00:00:00Z", Payload: o.payload}, nil)
	}, nil
}

// EvolveAdditive reaches the driver through a pipeline — issue #41.
//
// MySQL has its own CheckDestination and its own Evolve field, so it had the
// same defect Postgres did and needed the same fix: the early check refused
// the one difference the load had been told to repair, and the driver's
// evolve, which runs inside Write, never saw it.
func TestIntegrationEvolveAdditiveReachesTheDriver(t *testing.T) {
	db := open(t)
	name := table(t, db, "ingestion_id VARCHAR(36) NOT NULL, a TEXT")

	declared := sdk.Schema{
		{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "a", Type: sdk.TypeString},
		{Name: "b", Type: sdk.TypeString}, // the table does not have it
	}
	record := map[string]any{"ingestion_id": "id-1", "a": "x", "b": "new"}

	if err := sdk.Execute(context.Background(), &sdk.Pipeline{
		Source: sdk.Source{From: oneRow{record}},
		Target: sdk.Target{
			To:     tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive},
			Schema: declared,
		},
	}, nil); err != nil {
		t.Fatalf("the pipeline refused a column EvolveAdditive was asked to add: %v", err)
	}

	var got string
	if err := db.QueryRow("SELECT b FROM " + name).Scan(&got); err != nil {
		t.Fatalf("reading the column that should have been added: %v", err)
	}
	if got != "new" {
		t.Errorf("the new column holds %q, want %q", got, "new")
	}
}

// And with no evolution asked for, the early refusal is exactly what it was.
func TestIntegrationWithoutEvolveTheEarlyRefusalStands(t *testing.T) {
	db := open(t)
	name := table(t, db, "ingestion_id VARCHAR(36) NOT NULL, a TEXT")

	err := sdk.Execute(context.Background(), &sdk.Pipeline{
		Source: sdk.Source{From: oneRow{map[string]any{"ingestion_id": "id-1", "a": "x", "b": "new"}}},
		Target: sdk.Target{
			To: tomy.Table{DSN: dsn(t), Name: name},
			Schema: sdk.Schema{
				{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
				{Name: "a", Type: sdk.TypeString},
				{Name: "b", Type: sdk.TypeString},
			},
		},
	}, nil)
	if err == nil {
		t.Fatal("a declaration naming a column the table lacks was accepted " +
			"with no evolution asked for: the early check stopped checking")
	}
	if !strings.Contains(err.Error(), "b") || !strings.Contains(err.Error(), "does not have") {
		t.Errorf("the refusal no longer names the column: %v", err)
	}
}

// columnTypes is what the server actually created, by column.
func columnTypes(t *testing.T, db *sql.DB, name string) map[string]string {
	t.Helper()
	rows, err := db.Query(`
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ?
		ORDER BY ordinal_position`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var col, typ string
		if err := rows.Scan(&col, &typ); err != nil {
			t.Fatal(err)
		}
		out[col] = typ
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// EvolveAdditiveFromPayload on MySQL: the batch teaches the table a column.
//
// The same claims as the Postgres slice, against the destination whose
// `string` is LONGTEXT. That matters for what this does NOT do — see
// TestADiscoveredColumnIsNeverKeyed below.
func TestTheBatchTeachesTheTableAColumn(t *testing.T) {
	db := open(t)
	ctx := context.Background()

	name := table(t, db, "brevis_ingestion_id VARCHAR(36) NOT NULL,"+
		"source_key VARCHAR(64), valor LONGTEXT")

	declared := sdk.Schema{
		{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "source_key", Type: sdk.TypeString},
		{Name: "valor", Type: sdk.TypeString},
	}
	to := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	// Run one: nothing new. The table must not grow a column.
	if _, err := to.Write(ctx, []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-1", "source_key": "21129|01/12/2025", "valor": "8.89",
	}}}, opt); err != nil {
		t.Fatalf("run one: %v", err)
	}
	if got := columnTypes(t, db, name); len(got) != 3 {
		t.Fatalf("the table grew without being taught anything: %v", got)
	}

	// Run two: two fields nobody declared, one of them an object.
	if _, err := to.Write(ctx, []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-2", "source_key": "21129|02/12/2025", "valor": "8.90",
		"series": "21129",
		"meta":   map[string]any{"uf": "SP", "fonte": "bacen"},
	}}}, opt); err != nil {
		t.Fatalf("run two: %v", err)
	}

	types := columnTypes(t, db, name)
	if types["series"] != "longtext" {
		t.Errorf("series is %q, want longtext -- MySQL's `string` is LONGTEXT, "+
			"and a scalar is STRING", types["series"])
	}
	if types["meta"] != "json" {
		t.Errorf("meta is %q, want json -- an object is JSON, and MySQL has a "+
			"native type for it", types["meta"])
	}

	// The VALUE followed the column. A column added and left empty is the
	// failure that looks like success.
	var series, meta *string
	if err := db.QueryRowContext(ctx,
		"SELECT series, meta FROM "+name+" WHERE brevis_ingestion_id = 'id-2'",
	).Scan(&series, &meta); err != nil {
		t.Fatal(err)
	}
	if series == nil || *series != "21129" {
		t.Errorf("series = %v, want 21129", series)
	}
	if meta == nil || !strings.Contains(*meta, `"uf"`) {
		t.Errorf("meta = %v", meta)
	}

	// The row from run one is still there, with NULL in the new columns.
	var old *string
	if err := db.QueryRowContext(ctx,
		"SELECT series FROM "+name+" WHERE brevis_ingestion_id = 'id-1'").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old != nil {
		t.Errorf("the row written before the column existed has %v in it: "+
			"ADD COLUMN backfilled, which invents history", *old)
	}
}

// A field only the LAST record of a batch carries is still a column.
func TestTheBatchIsReadWhole(t *testing.T) {
	db := open(t)
	name := table(t, db, "brevis_ingestion_id VARCHAR(36) NOT NULL, source_key VARCHAR(64)")

	declared := sdk.Schema{
		{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "source_key", Type: sdk.TypeString},
	}
	to := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	batch := make([]sdk.Envelope, 50)
	for i := range batch {
		batch[i] = sdk.Envelope{Payload: map[string]any{
			"brevis_ingestion_id": fmt.Sprintf("id-%d", i),
			"source_key":          fmt.Sprintf("k-%d", i),
		}}
	}
	batch[49] = sdk.Envelope{Payload: map[string]any{
		"brevis_ingestion_id": "id-49", "source_key": "k-49", "late": "arrived last",
	}}

	if _, err := to.Write(context.Background(), batch, opt); err != nil {
		t.Fatalf("a field carried only by the last record: %v", err)
	}
	if _, ok := columnTypes(t, db, name)["late"]; !ok {
		t.Error("the table has no `late` column: the discovery read records[0] " +
			"instead of the batch")
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Errorf("%d rows, want 50 -- a record missing a discovered column "+
			"writes NULL there", n)
	}
}

// The mode completes the declaration; it does not switch the check off.
func TestTheModeCompletesTheDeclarationRatherThanSilencingIt(t *testing.T) {
	db := open(t)
	name := table(t, db, "brevis_ingestion_id VARCHAR(36) NOT NULL, prometida LONGTEXT")

	declared := sdk.Schema{
		{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "prometida", Type: sdk.TypeString},
	}
	to := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	_, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-1", "achada": "x",
	}}}, opt)
	if err == nil {
		t.Fatal("a declared column the chain does not produce was accepted: " +
			"the row check was skipped instead of extended")
	}
	if !strings.Contains(err.Error(), "prometida") {
		t.Errorf("the refusal does not name the declared column: %v", err)
	}
	if strings.Contains(err.Error(), "which Columns does not declare") {
		t.Errorf("the batch's own column was refused as undeclared: %v", err)
	}
}

// EvolveAdditive is unchanged: it adds what the DECLARATION has, never what
// the batch carries.
func TestTheBatchTeachesNothingWithoutTheMode(t *testing.T) {
	db := open(t)
	name := table(t, db, "brevis_ingestion_id VARCHAR(36) NOT NULL")

	declared := sdk.Schema{{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true}}
	to := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive}

	_, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-1", "nao_declarada": "x",
	}}}, sdk.WriteOptions{Schema: declared, Columns: declared.Names()})
	if err == nil {
		t.Fatal("EvolveAdditive accepted a field nothing declared")
	}
	if !strings.Contains(err.Error(), "nao_declarada") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// A discovered column is never keyed, and on MySQL that is the difference
// between working and not existing.
//
// MySQL's `string` is LONGTEXT, and MySQL will not key a LONGTEXT without a
// length:
//
//	Error 1170 (42000): BLOB/TEXT column used in key specification without a
//	key length
//
// Which is why `LandingOptions{UniqueID: true}` cannot be created there at
// all. Discovery never asks for a key — it only ever emits `add` — so it does
// not meet that wall and does not need to. This test is here so that stays
// true by assertion rather than by nobody having tried: a discovered column
// arrives NULLABLE and unindexed, whatever the batch looked like.
func TestADiscoveredColumnIsNeverKeyed(t *testing.T) {
	db := open(t)
	name := table(t, db, "brevis_ingestion_id VARCHAR(36) NOT NULL")

	declared := sdk.Schema{{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true}}
	to := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}

	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-1", "achada": "x",
	}}}, sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("a discovered LONGTEXT column: %v", err)
	}

	var nullable, key string
	if err := db.QueryRow(`
		SELECT is_nullable, column_key FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'achada'`,
		name).Scan(&nullable, &key); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" {
		t.Errorf("the discovered column is NOT NULL: the rows already in the "+
			"table have no value for it, and no value the SDK could invent is "+
			"true of them (is_nullable=%s)", nullable)
	}
	if key != "" {
		t.Errorf("the discovered column is indexed (column_key=%q): MySQL will "+
			"not key a LONGTEXT without a length, and a discovery that asked "+
			"for one would fail with error 1170 on a column nobody declared", key)
	}
}

// The mode also reaches the CREATE path, and the column it creates there is
// nullable.
//
// This is where `Required` on a discovered column would bite, and nowhere
// else: AlterTable forces an added column nullable whatever the declaration
// says, so a table that already exists hides the mistake entirely. A table
// created from the extended declaration does not.
func TestATableCreatedFromTheBatchTakesNullableColumns(t *testing.T) {
	db := open(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + name) })

	declared := sdk.Schema{{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true}}
	to := tomy.Table{
		DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload,
	}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-1", "achada": "x",
	}}}, opt); err != nil {
		t.Fatalf("creating from the batch: %v", err)
	}

	var nullable string
	if err := db.QueryRow(`
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'achada'`,
		name).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" {
		t.Errorf("the created column is NOT NULL (is_nullable=%s): the NEXT "+
			"batch need not carry the field, and the database would refuse it",
			nullable)
	}

	// And the next batch, without it, lands.
	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-2",
	}}}, opt); err != nil {
		t.Fatalf("the batch after the one that taught the table: %v", err)
	}
}

// The mode with nothing to complete is refused, and the table is not created.
//
// This is the hole S3 found by probing: with no Schema and no Columns the
// table came out `a longtext, b json` and nothing was said. On the landing
// layout that would have made brevis_received_at text instead of a timestamp
// and left brevis_loaded_at out entirely — it is a database DEFAULT and never
// travels in the row — so the end-to-end latency measurement would be gone,
// in a table that looks right.
func TestTheModeWithNothingToCompleteIsRefused(t *testing.T) {
	db := open(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + name) })

	to := tomy.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}

	// Nothing declared at all.
	_, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"a": "x", "b": map[string]any{"c": 1},
	}}}, sdk.WriteOptions{})
	if err == nil {
		t.Fatal("the whole table was created from the payload, with nothing " +
			"declared and nothing said")
	}
	if !strings.Contains(err.Error(), "COMPLETES a declaration") {
		t.Errorf("the refusal does not say what the mode is for: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the table was created anyway")
	}
}

// And it is refused BEFORE the extract, where it costs no source quota.
func TestTheModeWithNothingToCompleteIsRefusedBeforeTheExtract(t *testing.T) {
	// No DSN and no table: this must not need a destination to answer. A
	// refusal that depends on connecting is a refusal that arrives late.
	err := tomy.Table{Name: "bronze.bacen", Evolve: sdk.EvolveAdditiveFromPayload}.
		CheckDestination(context.Background(), nil)
	if err == nil {
		t.Fatal("CheckDestination accepted the mode with nothing declared")
	}
	if !strings.Contains(err.Error(), "bronze.bacen") {
		t.Errorf("the refusal does not name the table: %v", err)
	}

	// EvolveAdditive is untouched: declaring nothing declares nothing and
	// checks nothing, which is the rule the rest of Target follows.
	if err := (tomy.Table{Name: "t", Evolve: sdk.EvolveAdditive}).
		CheckDestination(context.Background(), nil); err != nil {
		t.Errorf("EvolveAdditive with no declaration was refused: %v", err)
	}
}

// A landing pipeline declares the same columns a gateway would.
//
// The end of the seam this whole marker exists for. The pipeline renders in
// its TRANSFORMER, so the driver never sees the object — and without the
// marker it declared `meta LONGTEXT` where a gateway declares `meta JSON`.
// Measured, before the fix:
//
//	colunas criadas: map[... meta:longtext tags:longtext ...]
//
// On a shared table that is a wall, not a cosmetic difference: the second
// writer is refused with "meta is json in the table and string in the
// declaration".
func TestALandingPipelineDeclaresWhatAGatewayWould(t *testing.T) {
	db := open(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + name) })

	record := map[string]any{
		"source_key": "21129|01/12/2025",
		"valor":      "8.89",
		"meta":       map[string]any{"uf": "SP"},
		"tags":       []any{"a", "b"},
	}
	out, err := sdk.Landing(name, sdk.LandingKey("source_key"), sdk.LandingColumns())(record)
	if err != nil {
		t.Fatal(err)
	}

	declared := sdk.LandingControlColumns(sdk.LandingOptions{})
	to := tomy.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: out}},
		sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("landing through the transformer: %v", err)
	}

	types := columnTypes(t, db, name)
	for _, c := range []struct{ name, want string }{
		{"meta", "json"}, {"tags", "json"},
		{"valor", "longtext"}, {"source_key", "longtext"},
	} {
		if types[c.name] != c.want {
			t.Errorf("%s is %q, want %q -- a gateway declares it %q for the "+
				"same record, and a shared table refuses the second writer",
				c.name, types[c.name], c.want, c.want)
		}
	}

	// And the JSON actually arrived as JSON, not as a quoted string inside a
	// JSON column. MySQL would take `"{\"uf\":\"SP\"}"` happily and every
	// JSON_EXTRACT afterwards would return nothing.
	var uf *string
	if err := db.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(meta, '$.uf')) FROM " + name).
		Scan(&uf); err != nil {
		t.Fatal(err)
	}
	if uf == nil || *uf != "SP" {
		t.Errorf("JSON_EXTRACT(meta,'$.uf') = %v, want SP -- the column holds "+
			"JSON, but not the JSON somebody can query", uf)
	}
}

// A column a batch created says so in the table itself.
//
// MySQL has no COMMENT ON COLUMN: the comment rides inside the ADD, which is
// what InlineComment is for.
func TestADiscoveredColumnCarriesItsOrigin(t *testing.T) {
	db := open(t)
	name := table(t, db, "brevis_ingestion_id VARCHAR(36) NOT NULL")

	declared := sdk.Schema{{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true}}
	to := tomy.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: map[string]any{
		"brevis_ingestion_id": "id-1", "series": "21129",
	}}}, sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatal(err)
	}

	var note string
	if err := db.QueryRow(`
		SELECT column_comment FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'series'`,
		name).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "batch") {
		t.Errorf("the column comment is %q: nothing in the table says nobody "+
			"declared it", note)
	}
}
