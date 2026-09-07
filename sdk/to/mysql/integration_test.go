package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
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

	first1, err := target.Write(context.Background(), l, opt)
	if err != nil {
		t.Fatalf("primeira: %v", err)
	}
	if first1.RowsLoaded != 5 {
		t.Errorf("primeira: %d carregadas", first1.RowsLoaded)
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

	esperado := map[string]any{
		"numerico": "123456789012345678.99",
		"data":     "2026-09-05",
		"instante": "2026-09-05T12:30:00Z",
		"inteiro":  int64(42),
		"texto":    "ola",
		"vazio":    nil,
	}
	for field, quero := range esperado {
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

	if first1 := runIt(); first1.Rows != 100 {
		t.Errorf("primeira carga: %d linhas, esperado 100", first1.Rows)
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
