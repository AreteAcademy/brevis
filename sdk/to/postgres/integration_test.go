package postgres_test

import (
	"context"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk"
	frompg "github.com/AreteAcademy/brevis/sdk/from/postgres"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// The integration tests are gated on a variable, like BigQuery's: without it
// they skip, and the normal suite stays offline.
//
//	docker compose -f docker-compose.drivers.yml up -d postgres
//	BREVIS_IT_PG_DSN=postgres://brevis:brevis@localhost:55432/brevis_it go test ./sdk/to/postgres/
func dsn(t *testing.T) string {
	t.Helper()
	d := os.Getenv("BREVIS_IT_PG_DSN")
	if d == "" {
		t.Skip("BREVIS_IT_PG_DSN não definida; suba o docker-compose.drivers.yml")
	}
	return d
}

func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// table creates a throwaway table and returns its name.
func table(t *testing.T, conn *pgx.Conn, ddl string) string {
	t.Helper()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	if _, err := conn.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s (%s)", name, ddl)); err != nil {
		t.Fatalf("criando %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
	})
	return name
}

func env(payload map[string]any) sdk.Envelope { return sdk.Envelope{Payload: payload} }

const defaultColumns = `
	ingestion_id TEXT NOT NULL,
	ingestion_loaded_at TIMESTAMPTZ NOT NULL,
	provider TEXT,
	source_key TEXT,
	valor NUMERIC(18,2)`

func loteDeTeste(n int) []sdk.Envelope {
	out := make([]sdk.Envelope, n)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range out {
		out[i] = env(map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%04d", i),
			"ingestion_loaded_at": now,
			"provider":            "teste",
			"source_key":          fmt.Sprintf("k%d", i),
			"valor":               "10.50",
		})
	}
	return out
}

// TestIntegrationARowActuallyGoesIn exists because the in-memory tests
// prove the bytes we assembled, not what the server accepts.
func TestIntegrationARowActuallyGoesIn(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, defaultColumns)

	res, err := topg.Table{DSN: dsn(t), Name: name}.Write(
		context.Background(), loteDeTeste(3), sdk.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.RowsLoaded != 3 {
		t.Errorf("RowsLoaded = %d, esperado 3", res.RowsLoaded)
	}

	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("o servidor tem %d rows, esperado 3", n)
	}
}

// TestIntegrationTheTablesOrderIsWhatCounts proves, end to end, that every value
// lands in the right column when the table's order is not the record's.
func TestIntegrationTheTablesOrderIsWhatCounts(t *testing.T) {
	conn := connect(t)
	// The table's order is deliberately different from the order the record is
	// usually written in.
	name := table(t, conn, `
		valor NUMERIC(18,2),
		provider TEXT,
		ingestion_loaded_at TIMESTAMPTZ NOT NULL,
		ingestion_id TEXT NOT NULL`)

	lote := []sdk.Envelope{env(map[string]any{
		"ingestion_id":        "abc",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"provider":            "acme",
		"valor":               "99.90",
	})}
	if _, err := (topg.Table{DSN: dsn(t), Name: name}).Write(
		context.Background(), lote, sdk.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var id, prov, value string
	err := conn.QueryRow(context.Background(),
		"SELECT ingestion_id, provider, valor::text FROM "+name).Scan(&id, &prov, &value)
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc" || prov != "acme" || value != "99.90" {
		t.Errorf("valores trocados de coluna: id=%q provider=%q valor=%q", id, prov, value)
	}
}

// TestIntegrationDedupLoadsTheSameBatchTwice is BigQuery's test ported, and it
// is phase 2's done criterion.
func TestIntegrationDedupLoadsTheSameBatchTwice(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, defaultColumns)
	if _, err := conn.Exec(context.Background(),
		fmt.Sprintf("CREATE UNIQUE INDEX ON %s (ingestion_id)", name)); err != nil {
		t.Fatal(err)
	}

	target := topg.Table{DSN: dsn(t), Name: name}
	lote := loteDeTeste(5)
	opt := sdk.WriteOptions{Dedup: sdk.DedupMerge}

	first, err := target.Write(context.Background(), lote, opt)
	if err != nil {
		t.Fatalf("primeira carga: %v", err)
	}
	if first.RowsLoaded != 5 || first.RowsIgnored != 0 {
		t.Errorf("primeira: %d carregadas, %d ignoradas", first.RowsLoaded, first.RowsIgnored)
	}

	segunda, err := target.Write(context.Background(), lote, opt)
	if err != nil {
		t.Fatalf("segunda carga: %v", err)
	}
	if segunda.RowsLoaded != 0 || segunda.RowsIgnored != 5 {
		t.Errorf("segunda: %d carregadas, %d ignoradas -- esperava 0 e 5",
			segunda.RowsLoaded, segunda.RowsIgnored)
	}

	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("a tabela tem %d rows depois de duas cargas do mesmo lote", n)
	}
}

// TestIntegrationDedupWithoutAnIndexRefuses: with no unique index, ON CONFLICT
// has nothing to match and every run would insert duplicates.
func TestIntegrationDedupWithoutAnIndexRefuses(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, defaultColumns)

	_, err := topg.Table{DSN: dsn(t), Name: name}.Write(
		context.Background(), loteDeTeste(1), sdk.WriteOptions{Dedup: sdk.DedupMerge})
	if err == nil {
		t.Fatal("dedup sem índice único passou")
	}
	for _, exigido := range []string{"unique index", "CREATE UNIQUE INDEX"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro não diz %q: %v", exigido, err)
		}
	}
}

// TestIntegrationAFieldTheTableLacksIsRefused: the server would refuse too, but
// with `column "x" of relation "y" does not exist` in the middle of a COPY --
// after the whole extract, and without saying what to do. What Reconcile buys is
// refusing BEFORE, with the way out written down.
func TestIntegrationAFieldTheTableLacksIsRefused(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, defaultColumns)

	lote := []sdk.Envelope{env(map[string]any{
		"ingestion_id":        "x",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"coluna_inexistente":  1,
	})}
	_, err := topg.Table{DSN: dsn(t), Name: name}.Write(context.Background(), lote, sdk.WriteOptions{})
	if err == nil {
		t.Fatal("campo sem coluna passou")
	}
	for _, exigido := range []string{
		"coluna_inexistente",
		"add the column to the table, or remove the field in Transform",
	} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro não diz %q -- provavelmente quem recusou foi o "+
				"servidor, e não o Reconcile: %v", exigido, err)
		}
	}
}

// TestIntegrationAMissingTableSaysHowToCreateIt: the driver does not create and
// infers no types, so the error has to give what is missing for the DDL to come
// out of one reading.
func TestIntegrationAMissingTableSaysHowToCreateIt(t *testing.T) {
	_, err := topg.Table{DSN: dsn(t), Name: "nao_existe_mesmo"}.Write(
		context.Background(), loteDeTeste(1), sdk.WriteOptions{})
	if err == nil {
		t.Fatal("tabela ausente passou")
	}
	for _, exigido := range []string{"does not exist", "ingestion_id", "source_key"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro não diz %q: %v", exigido, err)
		}
	}
}

// TestIntegrationTheReadIsStreamed FAILS if the driver buffers instead of
// streaming.
//
// It consumes one row and stops. If the driver built the whole list before
// returning, it would have read all fifty thousand -- and the time would show
// it.
func TestIntegrationTheReadIsStreamed(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, "i INT, texto TEXT")
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		`INSERT INTO %s SELECT g, repeat('x', 500) FROM generate_series(1, 50000) g`, name)); err != nil {
		t.Fatal(err)
	}

	source := frompg.Query{DSN: dsn(t), SQL: "SELECT i, texto FROM " + name + " ORDER BY i"}
	seq, err := source.Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	start := time.Now()
	recebeu := false
	for _, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		recebeu = true
		break // one row only, on purpose: it is what exposes the buffer
	}
	if !recebeu {
		t.Fatal("nenhuma linha")
	}

	// The first row has to arrive without waiting for the 50 thousand. The limit
	// is generous on purpose: what it catches is the difference between a stream
	// and a buffer, not the
	// velocidade do servidor.
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a primeira linha levou %s; o driver parece bufferizar", d)
	}
}

// TestIntegrationTheTypesComeFromTheServer proves §3.1's table against a real
// Postgres, and not against the values we built ourselves.
func TestIntegrationTheTypesComeFromTheServer(t *testing.T) {
	source := frompg.Query{DSN: dsn(t), SQL: `
		SELECT
			123456789012345678.99::numeric   AS numerico,
			'2026-09-05'::date               AS data,
			'2026-09-05 12:30:00+00'::timestamptz AS instante,
			'\xdeadbeef'::bytea              AS bytes,
			'{"a":[1,2]}'::jsonb             AS documento,
			'178d0b49-dece-5738-b8eb-f5cae2221aea'::uuid AS identificador,
			NULL::text                       AS vazio,
			ARRAY[1,2,3]                     AS numeros`}

	seq, err := source.Read(context.Background(), sdk.ReadOptions{})
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
		"numerico":      "123456789012345678.99",
		"data":          "2026-09-05",
		"instante":      "2026-09-05T12:30:00Z",
		"identificador": "178d0b49-dece-5738-b8eb-f5cae2221aea",
		"vazio":         nil,
	}
	for field, quero := range expected {
		if got := line[field]; got != quero {
			t.Errorf("%s = %#v, esperado %#v", field, got, quero)
		}
	}
	if _, ok := line["documento"].(map[string]any); !ok {
		t.Errorf("documento = %#v; JSONB devia chegar aninhado", line["documento"])
	}
	if b, ok := line["bytes"].([]byte); !ok || len(b) != 4 {
		t.Errorf("bytes = %#v; BYTEA devia chegar como []byte", line["bytes"])
	}
}

// TestIntegrationPostgresToPostgres is phase 2's done criterion: the whole
// pipeline, with dedup proven by loading the same batch twice.
func TestIntegrationPostgresToPostgres(t *testing.T) {
	conn := connect(t)

	src := table(t, conn, "id INT, nome TEXT, valor NUMERIC(18,2), atualizado_em TIMESTAMPTZ")
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		`INSERT INTO %s SELECT g, 'registro ' || g, (g * 1.5)::numeric, now() FROM generate_series(1, 100) g`,
		src)); err != nil {
		t.Fatal(err)
	}

	target := table(t, conn, `
		ingestion_id TEXT NOT NULL,
		ingestion_loaded_at TIMESTAMPTZ NOT NULL,
		provider TEXT NOT NULL,
		entity TEXT NOT NULL,
		source_key TEXT NOT NULL,
		record_ts TEXT NOT NULL,
		nome TEXT,
		valor NUMERIC(18,2)`)
	if _, err := conn.Exec(context.Background(),
		fmt.Sprintf("CREATE UNIQUE INDEX ON %s (ingestion_id)", target)); err != nil {
		t.Fatal(err)
	}

	runIt := func() *sdk.Result {
		t.Helper()
		data, err := sdk.Extract(context.Background(), sdk.Source{
			From: frompg.Query{
				DSN: dsn(t),
				SQL: "SELECT id, nome, valor, atualizado_em FROM " + src + " ORDER BY id",
			},
		})
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		data = sdk.Transform(data,
			// The source_key comes from the id as TEXT: the ingestion_id's key
			// is composed by concatenation, and a number and its string have
			// to produce the same id.
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return fmt.Sprint(r["id"]), nil
			}),
			sdk.Without("id"),
			sdk.Rename(map[string]string{"atualizado_em": "record_ts"}),
			sdk.Compute("provider", func(map[string]any) (any, error) { return "pg", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "registros", nil }),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		)
		res, err := sdk.Load(context.Background(), data, sdk.Target{
			To: topg.Table{DSN: dsn(t), Name: target},
			Columns: []string{
				"ingestion_id", "ingestion_loaded_at", "provider", "entity",
				"source_key", "record_ts", "nome", "valor",
			},
			Dedup: sdk.DedupMerge,
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return res
	}

	first := runIt()
	if first.Rows != 100 {
		t.Errorf("primeira carga: %d rows, esperado 100", first.Rows)
	}

	segunda := runIt()
	if segunda.Rows != 0 || segunda.Ignored != 100 {
		t.Errorf("segunda carga: %d carregadas e %d ignoradas -- o mesmo lote devia ser todo dedup",
			segunda.Rows, segunda.Ignored)
	}

	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 100 {
		t.Errorf("o destino tem %d rows depois de duas execuções idênticas", n)
	}

	// And the precision survived the whole crossing: NUMERIC in Postgres, a
	// string in the record, NUMERIC back again.
	var value string
	if err := conn.QueryRow(context.Background(),
		"SELECT valor::text FROM "+target+" WHERE source_key = '2'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "3.00" {
		t.Errorf("valor = %q, esperado \"3.00\"", value)
	}
}

// TestIntegrationCreateTableFromTheDeclaration.
//
// The gate this feature actually needs. Rendering a CREATE correctly and having
// the server refuse it is exactly the shape that let CreateSQL exist since
// v0.9.0 without ever being executed against BigQuery -- a unit test on the
// string proves the string.
//
// So: declare, create, LOAD, and read the rows back.
func TestIntegrationCreateTableFromTheDeclaration(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("public.created_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name)
	})

	schema := sdk.Schema{
		{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
		{Name: "sku", Type: sdk.TypeString, Required: true},
		{Name: "quantity", Type: sdk.TypeInt64},
		{Name: "price", Type: sdk.TypeNumeric},
		{Name: "active", Type: sdk.TypeBool, Default: true},
		{Name: "status", Type: sdk.TypeString, Default: "pending"},
		{Name: "attempts", Type: sdk.TypeInt64, Default: 0},
		{Name: "seen_at", Type: sdk.TypeTimestamp, Default: sdk.CurrentTimestamp},
		{Name: "payload", Type: sdk.TypeJSON},
	}

	row := env(map[string]any{
		"ingestion_id": "id-1", "ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"sku": "A1", "quantity": 3, "price": "9.90", "payload": map[string]any{"k": 1},
	})
	res, err := topg.Table{DSN: dsn(t), Name: name, CreateTable: true}.Write(
		ctx, []sdk.Envelope{row}, sdk.WriteOptions{Schema: schema, Columns: schema.Names()})
	if err != nil {
		t.Fatalf("the load failed: %v", err)
	}
	if !res.TableCreated {
		t.Error("the load did not report creating the table")
	}
	if res.RowsLoaded != 1 {
		t.Errorf("RowsLoaded = %d", res.RowsLoaded)
	}

	// The TYPES the server actually created, which is the half a string
	// assertion cannot reach.
	rows, err := conn.Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, strings.TrimPrefix(name, "public."))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := map[string][3]string{}
	for rows.Next() {
		var col, typ, nullable string
		var def *string
		if err := rows.Scan(&col, &typ, &nullable, &def); err != nil {
			t.Fatal(err)
		}
		d := ""
		if def != nil {
			d = *def
		}
		got[col] = [3]string{typ, nullable, d}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for col, want := range map[string][3]string{
		"sku":      {"text", "NO", ""},
		"quantity": {"bigint", "YES", ""},
		"price":    {"numeric", "YES", ""},
		"active":   {"boolean", "YES", "true"},
		"status":   {"text", "YES", "'pending'::text"},
		"attempts": {"bigint", "YES", "0"},
		"seen_at":  {"timestamp with time zone", "YES", "CURRENT_TIMESTAMP"},
		"payload":  {"jsonb", "YES", ""},
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
		if g[2] != want[2] {
			t.Errorf("%s default=%q, wanted %q", col, g[2], want[2])
		}
	}

	// And the DEFAULTS are real: a second row that omits them comes back with
	// the declared values, which is the only thing that proves the clause did
	// something.
	if _, err := conn.Exec(ctx,
		"INSERT INTO "+name+" (ingestion_id, ingestion_loaded_at, sku) VALUES ('x', now(), 'B2')"); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int64
	var active bool
	if err := conn.QueryRow(ctx,
		"SELECT status, attempts, active FROM "+name+" WHERE sku = 'B2'").
		Scan(&status, &attempts, &active); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 0 || !active {
		t.Errorf("the defaults did not apply: status=%q attempts=%d active=%v",
			status, attempts, active)
	}
}

// CreateTable with no Schema and no CreateSQL is refused naming BOTH ways out.
// It used to be impossible to reach; now it is a wrong configuration, and a
// wrong configuration that says nothing is a support ticket.
func TestIntegrationCreateTableWithNothingToCreateFrom(t *testing.T) {
	name := fmt.Sprintf("public.nothing_%d", time.Now().UnixNano())
	_, err := topg.Table{DSN: dsn(t), Name: name, CreateTable: true}.Write(
		context.Background(),
		[]sdk.Envelope{env(map[string]any{"ingestion_id": "x",
			"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339), "sku": "A1"})},
		sdk.WriteOptions{Columns: []string{"ingestion_id", "ingestion_loaded_at", "sku"}})
	if err == nil {
		t.Fatal("a table was created with nothing describing it")
	}
	for _, want := range []string{"Schema", "CreateSQL", "does not infer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestIntegrationEvolveAddsAColumnAndKeepsTheRows.
//
// The whole point of the feature, against a real server: a vendor adds a field,
// the declaration grows, and the load carries on — without touching the rows
// that are already there.
func TestIntegrationEvolveAddsAColumnAndKeepsTheRows(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("public.evolved_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

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
		return env(p)
	}

	// The table as it is today.
	writer := topg.Table{DSN: dsn(t), Name: name, CreateTable: true}
	if _, err := writer.Write(ctx, []sdk.Envelope{row("a", "A1", nil)},
		sdk.WriteOptions{Schema: base, Columns: base.Names()}); err != nil {
		t.Fatalf("the first load failed: %v", err)
	}

	// The vendor adds two fields.
	grown := append(append(sdk.Schema{}, base...),
		sdk.Column{Name: "channel", Type: sdk.TypeString, Default: "web"},
		sdk.Column{Name: "score", Type: sdk.TypeFloat64},
	)

	// Without Evolve, the load REFUSES and says how to allow it.
	_, err := topg.Table{DSN: dsn(t), Name: name}.Write(
		ctx, []sdk.Envelope{row("b", "B2", map[string]any{"score": 1.5})},
		sdk.WriteOptions{Schema: grown, Columns: grown.Names()})
	if err == nil {
		t.Fatal("a table missing two declared columns was written to anyway")
	}
	if !strings.Contains(err.Error(), "EvolveAdditive") {
		t.Errorf("the refusal does not say how to allow it: %v", err)
	}

	// With it, the columns arrive and the load goes through.
	res, err := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive}.Write(
		ctx, []sdk.Envelope{row("b", "B2", map[string]any{"score": 1.5})},
		sdk.WriteOptions{Schema: grown, Columns: grown.Names()})
	if err != nil {
		t.Fatalf("the evolving load failed: %v", err)
	}
	if res.RowsLoaded != 1 {
		t.Errorf("RowsLoaded = %d", res.RowsLoaded)
	}

	// The row that was already there kept its data, and the new column is NULL
	// on it -- NOT the default.
	//
	// This is the assertion that found a real bug. `ADD COLUMN ... DEFAULT x`
	// backfills every existing row, so the March row came back claiming
	// channel="web" -- a value it never had. The ADD and the SET DEFAULT are
	// two statements now, and the unit test could not have caught it: the
	// statement it asserted was perfectly well formed.
	var channel *string
	var sku string
	if err := conn.QueryRow(ctx,
		"SELECT sku, channel FROM "+name+" WHERE ingestion_id = 'a'").Scan(&sku, &channel); err != nil {
		t.Fatal(err)
	}
	if sku != "A1" {
		t.Errorf("the existing row changed: sku=%q", sku)
	}
	if channel != nil {
		t.Errorf("the existing row was backfilled with %q", *channel)
	}

	// And the added column is NULLABLE with its declared default, whatever the
	// declaration said about NOT NULL: no value this SDK could invent is true
	// of rows that already exist.
	var nullable, def string
	if err := conn.QueryRow(ctx, `
		SELECT is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema='public' AND table_name=$1 AND column_name='channel'`,
		strings.TrimPrefix(name, "public.")).Scan(&nullable, &def); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" {
		t.Errorf("the added column is %s nullable", nullable)
	}
	if !strings.Contains(def, "web") {
		t.Errorf("the added column lost its default: %q", def)
	}
}

// TestIntegrationEvolveRefusesToNarrow.
//
// numeric -> float64 is the one somebody always asks for, and it is the one
// that silently rounds money. It is refused naming both types.
func TestIntegrationEvolveRefusesToNarrow(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("public.narrow_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	base := sdk.Schema{
		{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
		{Name: "amount", Type: sdk.TypeNumeric},
	}
	first := env(map[string]any{
		"ingestion_id": "a", "ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"amount": "10.50",
	})
	writer := topg.Table{DSN: dsn(t), Name: name, CreateTable: true}
	if _, err := writer.Write(ctx, []sdk.Envelope{first},
		sdk.WriteOptions{Schema: base, Columns: base.Names()}); err != nil {
		t.Fatal(err)
	}

	narrowed := sdk.Schema{base[0], base[1], {Name: "amount", Type: sdk.TypeFloat64}}
	_, err := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive}.Write(
		ctx, []sdk.Envelope{first}, sdk.WriteOptions{Schema: narrowed, Columns: narrowed.Names()})
	if err == nil {
		t.Fatal("a numeric column was narrowed to float")
	}
	for _, want := range []string{"amount", "numeric", "float64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	// And the column is untouched.
	var typ string
	if err := conn.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema='public' AND table_name=$1 AND column_name='amount'`,
		strings.TrimPrefix(name, "public.")).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "numeric" {
		t.Errorf("the refused change was applied anyway: the column is %s", typ)
	}
}

// oneRow is a source of exactly one record, so a pipeline can be run without
// a vendor behind it.
type oneRow struct{ payload map[string]any }

func (oneRow) Describe() string { return "one row" }
func (o oneRow) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		yield(sdk.Envelope{Provider: "p", Entity: "e", SourceKey: "k",
			RecordTS: "2026-09-29T00:00:00Z", Payload: o.payload}, nil)
	}, nil
}

// EvolveAdditive reaches the driver through a pipeline — issue #41, fixed.
//
// This test was the inverse of itself: it asserted the defect, and its own
// message said to invert it the day the pipeline stopped refusing. That day
// is this commit.
//
// The defect was ORDERING. `checkDestination` runs before the extract and
// refused every declared column the table lacks; the driver's `evolve` runs
// inside Write and would have added it. The flag was unreachable in the only
// case it exists for, because a declaration that adds a column is the only
// way to ask for one.
func TestIntegrationEvolveAdditiveReachesTheDriver(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, `ingestion_id TEXT NOT NULL, a TEXT`)

	declared := sdk.Schema{
		{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "a", Type: sdk.TypeString},
		{Name: "b", Type: sdk.TypeString}, // the table does not have it
	}
	record := map[string]any{"ingestion_id": "id-1", "a": "x", "b": "new"}

	if err := sdk.Execute(context.Background(), &sdk.Pipeline{
		Source: sdk.Source{From: oneRow{record}},
		Target: sdk.Target{
			To:     topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive},
			Schema: declared,
		},
	}, nil); err != nil {
		t.Fatalf("the pipeline refused a column EvolveAdditive was asked to add: %v", err)
	}

	var has bool
	if err := conn.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                WHERE table_name = $1 AND column_name = 'b')`,
		name).Scan(&has); err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("the run succeeded and the column is not there")
	}

	var got string
	if err := conn.QueryRow(context.Background(),
		"SELECT b FROM "+name).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "new" {
		t.Errorf("the new column holds %q, want %q -- the column was added and "+
			"the value did not follow it", got, "new")
	}
}

// And with no evolution asked for, the early refusal is exactly what it was.
//
// This is the property the fix had to keep. `CheckDestination` runs before
// the extract for a stated reason -- one information_schema query against a
// whole source quota spent to learn a column does not match -- and that
// reason still holds for every declaration the load was not told to repair.
func TestIntegrationWithoutEvolveTheEarlyRefusalStands(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, `ingestion_id TEXT NOT NULL, a TEXT`)

	err := sdk.Execute(context.Background(), &sdk.Pipeline{
		Source: sdk.Source{From: oneRow{map[string]any{"ingestion_id": "id-1", "a": "x", "b": "new"}}},
		Target: sdk.Target{
			// No Evolve: the zero value refuses any difference, and did
			// before the flag existed.
			To: topg.Table{DSN: dsn(t), Name: name},
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
	for _, want := range []string{"b", "does not have", "before the extract"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal no longer says %q: %v", want, err)
		}
	}

	var n int
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns WHERE table_name = $1`,
		name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("the table has %d columns, want 2: something altered it "+
			"without being asked", n)
	}
}

// EvolveAdditiveFromPayload: the batch teaches the table a column.
//
// The record is the consumer's own from issue #40. Run one lands what the
// table already has; run two carries a field nobody declared, and the column
// has to be there afterwards with the value in it.
func TestTheBatchTeachesTheTableAColumn(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	name := table(t, conn, `
		brevis_ingestion_id TEXT NOT NULL,
		source_key TEXT,
		valor TEXT`)

	declared := sdk.Schema{
		{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "source_key", Type: sdk.TypeString},
		{Name: "valor", Type: sdk.TypeString},
	}
	to := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	// Run one: nothing new. The table must not grow a column.
	if _, err := to.Write(ctx, []sdk.Envelope{env(map[string]any{
		"brevis_ingestion_id": "id-1", "source_key": "21129|01/12/2025", "valor": "8.89",
	})}, opt); err != nil {
		t.Fatalf("run one: %v", err)
	}
	if got := columnNames(t, conn, name); len(got) != 3 {
		t.Fatalf("the table grew without being taught anything: %v", got)
	}

	// Run two: two fields nobody declared, one of them an object.
	if _, err := to.Write(ctx, []sdk.Envelope{env(map[string]any{
		"brevis_ingestion_id": "id-2", "source_key": "21129|02/12/2025", "valor": "8.90",
		"series": "21129",
		"meta":   map[string]any{"uf": "SP", "fonte": "bacen"},
	})}, opt); err != nil {
		t.Fatalf("run two: %v", err)
	}

	types := columnTypes(t, conn, name)
	if types["series"] != "text" {
		t.Errorf("series is %q, want text -- a scalar is STRING", types["series"])
	}
	if types["meta"] != "jsonb" {
		t.Errorf("meta is %q, want jsonb -- an object is JSON", types["meta"])
	}

	// The VALUE followed the column. A column added and left empty is the
	// failure that looks like success.
	var series, meta *string
	if err := conn.QueryRow(ctx,
		"SELECT series, meta::text FROM "+name+" WHERE brevis_ingestion_id = 'id-2'",
	).Scan(&series, &meta); err != nil {
		t.Fatal(err)
	}
	if series == nil || *series != "21129" {
		t.Errorf("series = %v, want 21129", series)
	}
	if meta == nil || !strings.Contains(*meta, `"uf": "SP"`) {
		t.Errorf("meta = %v", meta)
	}

	// And the row from run one is still there, with NULL in the new columns.
	// ADD COLUMN only: nothing was rewritten and nothing was dropped.
	var old *string
	if err := conn.QueryRow(ctx,
		"SELECT series FROM "+name+" WHERE brevis_ingestion_id = 'id-1'").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old != nil {
		t.Errorf("the row written before the column existed has %v in it: "+
			"ADD COLUMN backfilled, which invents history", *old)
	}
}

// A field that only the LAST record of a batch carries is still a column.
//
// CheckRow looks at records[0], and a discovery that did the same would miss
// this — the row would then be refused by Reconcile at the destination,
// naming a field nobody declared.
func TestTheBatchIsReadWhole(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := table(t, conn, `brevis_ingestion_id TEXT NOT NULL, source_key TEXT`)

	to := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	declared := sdk.Schema{
		{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true},
		{Name: "source_key", Type: sdk.TypeString},
	}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	batch := make([]sdk.Envelope, 50)
	for i := range batch {
		batch[i] = env(map[string]any{
			"brevis_ingestion_id": fmt.Sprintf("id-%d", i),
			"source_key":          fmt.Sprintf("k-%d", i),
		})
	}
	batch[49] = env(map[string]any{
		"brevis_ingestion_id": "id-49", "source_key": "k-49", "late": "arrived last",
	})

	if _, err := to.Write(ctx, batch, opt); err != nil {
		t.Fatalf("a field carried only by the last record: %v", err)
	}
	if _, ok := columnTypes(t, conn, name)["late"]; !ok {
		t.Error("the table has no `late` column: the discovery read records[0] " +
			"instead of the batch")
	}

	// The 49 records that do not carry it are not an error, and they land.
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Errorf("%d rows, want 50 -- a record missing a discovered column "+
			"writes NULL there, which is what a landing table does", n)
	}
}

// EvolveAdditive is unchanged: it adds what the DECLARATION has, never what
// the batch carries.
func TestTheBatchTeachesNothingWithoutTheMode(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, `brevis_ingestion_id TEXT NOT NULL`)

	to := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditive}
	declared := sdk.Schema{{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true}}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	_, err := to.Write(context.Background(), []sdk.Envelope{env(map[string]any{
		"brevis_ingestion_id": "id-1", "nao_declarada": "x",
	})}, opt)
	if err == nil {
		t.Fatal("EvolveAdditive accepted a field nothing declared: the mode " +
			"that reads the payload is a different one, and it is opt-in")
	}
	if !strings.Contains(err.Error(), "nao_declarada") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// columnTypes is what the server actually created, by column.
func columnTypes(t *testing.T, conn *pgx.Conn, name string) map[string]string {
	t.Helper()
	rows, err := conn.Query(context.Background(), `
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, strings.TrimPrefix(name, "public."))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

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

func columnNames(t *testing.T, conn *pgx.Conn, name string) []string {
	t.Helper()
	types := columnTypes(t, conn, name)
	out := make([]string, 0, len(types))
	for k := range types {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The mode completes the declaration; it does not switch the check off.
//
// A mutation that replaced the extension with `if !FromPayload() { CheckRow }`
// passed every other test here, because once the batch's fields are declared
// there is nothing left for the undeclared half to catch. The half it loses
// is the other one: a column the CONSUMER declared and the chain does not
// produce. That is still a bug, and this mode has nothing to say about it.
func TestTheModeCompletesTheDeclarationRatherThanSilencingIt(t *testing.T) {
	conn := connect(t)
	name := table(t, conn, `brevis_ingestion_id TEXT NOT NULL, prometida TEXT`)

	to := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	declared := sdk.Schema{
		{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true},
		// Declared by the consumer and never produced by the chain.
		{Name: "prometida", Type: sdk.TypeString},
	}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}

	_, err := to.Write(context.Background(), []sdk.Envelope{env(map[string]any{
		"brevis_ingestion_id": "id-1", "achada": "x",
	})}, opt)
	if err == nil {
		t.Fatal("a declared column the chain does not produce was accepted: " +
			"the row check was skipped instead of extended, and the half this " +
			"mode has nothing to do with went with it")
	}
	if !strings.Contains(err.Error(), "prometida") {
		t.Errorf("the refusal does not name the declared column: %v", err)
	}
	// And the OTHER half is not what fired. The message lists the row's
	// fields as context, `achada` among them, which is not the same as
	// blaming it: "which Columns does not declare" is the undeclared half's
	// own wording, and that half has nothing left to say here.
	if strings.Contains(err.Error(), "which Columns does not declare") {
		t.Errorf("the batch's own column was refused as undeclared, which is "+
			"the thing this mode exists to stop: %v", err)
	}
}

// A landing pipeline declares the same columns a gateway would.
//
// The pipeline renders in its TRANSFORMER, so the driver never sees the
// object. Without the marker it declared `meta TEXT` where a gateway declares
// `meta JSON`, and on a shared table the second writer was refused outright.
func TestALandingPipelineDeclaresWhatAGatewayWould(t *testing.T) {
	conn := connect(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
	})

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
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: out}},
		sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("landing through the transformer: %v", err)
	}

	types := columnTypes(t, conn, name)
	for _, c := range []struct{ name, want string }{
		{"meta", "jsonb"}, {"tags", "jsonb"},
		{"valor", "text"}, {"source_key", "text"},
	} {
		if types[c.name] != c.want {
			t.Errorf("%s is %q, want %q -- a gateway declares it %q for the "+
				"same record", c.name, types[c.name], c.want, c.want)
		}
	}

	// And the JSON is the JSON somebody can query, not a quoted string
	// sitting inside a jsonb column.
	var uf *string
	if err := conn.QueryRow(context.Background(),
		"SELECT meta->>'uf' FROM "+name).Scan(&uf); err != nil {
		t.Fatal(err)
	}
	if uf == nil || *uf != "SP" {
		t.Errorf("meta->>'uf' = %v, want SP -- the column holds JSON, but not "+
			"the JSON somebody can query", uf)
	}
}

// A column a batch created says so in the table itself.
func TestADiscoveredColumnCarriesItsOrigin(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := table(t, conn, `brevis_ingestion_id TEXT NOT NULL`)

	declared := sdk.Schema{{Name: "brevis_ingestion_id", Type: sdk.TypeString, Required: true}}
	to := topg.Table{DSN: dsn(t), Name: name, Evolve: sdk.EvolveAdditiveFromPayload}
	if _, err := to.Write(ctx, []sdk.Envelope{env(map[string]any{
		"brevis_ingestion_id": "id-1", "series": "21129",
	})}, sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatal(err)
	}

	note := columnComment(t, conn, name, "series")
	if note == nil || !strings.Contains(*note, "batch") {
		t.Errorf("the column has no comment saying a batch brought it (%v): "+
			"six months from now the table is the only thing left, and a log "+
			"line has rotated", note)
	}

	// The declared column carries none: the consumer knows when they wrote it.
	declaredNote := columnComment(t, conn, name, "brevis_ingestion_id")
	if declaredNote != nil {
		t.Errorf("a declared column was commented: %v", *declaredNote)
	}
}

// columnComment is what the server records about a column.
func columnComment(t *testing.T, conn *pgx.Conn, table, column string) *string {
	t.Helper()
	var note *string
	if err := conn.QueryRow(context.Background(), `
		SELECT col_description(c.oid, a.attnum)
		FROM pg_class c
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relname = $1 AND a.attname = $2`, table, column).Scan(&note); err != nil {
		t.Fatal(err)
	}
	return note
}

// The `document` shape's `data` column is readable as JSON, not as a string
// of JSON.
//
// `data` is declared TypeJSON and carried a Go string until a consumer found
// what that means on BigQuery. Postgres parses a string into jsonb and always
// did, so this is the test that the fix for BigQuery did not break the
// destination that was already right.
func TestTheDocumentColumnIsQueryableJSON(t *testing.T) {
	conn := connect(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
	})

	out, err := sdk.Landing(name, sdk.LandingKey("id"))(map[string]any{
		"id": "A-1", "nested": map[string]any{"uf": "SP"},
	})
	if err != nil {
		t.Fatal(err)
	}

	declared := sdk.LandingSchema(sdk.LandingOptions{})
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true}
	if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: out}},
		sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("landing the document shape: %v", err)
	}

	var uf *string
	if err := conn.QueryRow(context.Background(),
		"SELECT data->'nested'->>'uf' FROM "+name).Scan(&uf); err != nil {
		t.Fatal(err)
	}
	if uf == nil || *uf != "SP" {
		t.Errorf("data->'nested'->>'uf' = %v, want SP -- the column holds "+
			"JSON, but not the JSON somebody can query", uf)
	}
}

// A plain string into a jsonb column still lands, and is still queryable.
//
// BigQuery refuses one, because there the row is marshalled whole and the
// string becomes a JSON string literal. Postgres parses it, and has always
// done so. This pins that difference: a refusal generalised to every
// destination would break loads that are correct today, and the generalising
// is the tempting move.
func TestAStringIntoAJSONColumnIsFineHere(t *testing.T) {
	conn := connect(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
	})

	declared := sdk.Schema{
		{Name: "id", Type: sdk.TypeString, Required: true},
		{Name: "payload", Type: sdk.TypeJSON},
	}
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true}
	if _, err := to.Write(context.Background(), []sdk.Envelope{env(map[string]any{
		"id": "1", "payload": `{"uf":"SP"}`,
	})}, sdk.WriteOptions{Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("a string into jsonb: %v", err)
	}

	var uf *string
	if err := conn.QueryRow(context.Background(),
		"SELECT payload->>'uf' FROM "+name).Scan(&uf); err != nil {
		t.Fatal(err)
	}
	if uf == nil || *uf != "SP" {
		t.Errorf("payload->>'uf' = %v, want SP -- the server parses the text, "+
			"and the BigQuery refusal must not travel here", uf)
	}
}

// A table created under one prefix refuses a load declaring another.
//
// In a child process, because the prefix is resolved when the package loads.
// The parent creates the table with `brevis_` columns; the child runs with
// BREVIS_LANDING_PREFIX=acme and must be refused BEFORE the extract rather
// than quietly adding eight columns beside the eight already there.
func TestADifferentPrefixOnALiveTableIsRefused(t *testing.T) {
	const marker = "BREVIS_TEST_PGPREFIX_CHILD"

	if name := os.Getenv(marker); name != "" {
		declared := sdk.LandingSchema(sdk.LandingOptions{})
		err := topg.Table{DSN: dsn(t), Name: name}.
			CheckDestination(context.Background(), declared.Names())
		if err == nil {
			t.Fatal("accepted: eight columns would be added beside the eight " +
				"already there, and `partition by brevis_record_key` would " +
				"read a column nothing writes")
		}
		// Matched on what ONLY this refusal says. Without the check,
		// CheckDestination still fails -- the declared `acme_*` columns are
		// missing from a table full of `brevis_*` ones -- and that error
		// names both prefixes and the table too. A mutation removing the
		// check survived until this assertion existed.
		if !strings.Contains(err.Error(), "already carries the landing layout") {
			t.Fatalf("this is the missing-columns refusal, not the prefix "+
				"one: %v", err)
		}
		for _, want := range []string{"brevis_", "acme_", name, "rename"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %q: %v", want, err)
			}
		}
		return
	}

	conn := connect(t)
	// Created with the DEFAULT prefix, which is what this process resolved.
	declared := sdk.LandingSchema(sdk.LandingOptions{})
	ddl := make([]string, 0, len(declared))
	for _, c := range declared {
		ddl = append(ddl, c.Name+" TEXT")
	}
	name := table(t, conn, strings.Join(ddl, ","))

	cmd := exec.Command(os.Args[0], "-test.run=TestADifferentPrefixOnALiveTableIsRefused", "-test.v")
	cmd.Env = append(os.Environ(), marker+"="+name,
		"BREVIS_LANDING_PREFIX=acme", "BREVIS_IT_PG_DSN="+dsn(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_LANDING_PREFIX=acme:\n%s", out)
	}

	// And the table was not touched.
	if got := len(columnTypes(t, conn, name)); got != len(declared) {
		t.Errorf("the table has %d columns, want %d: the check ran too late",
			got, len(declared))
	}
}

// A table whose columns flattening would abandon refuses the load.
//
// The parent creates the table the way a stream landed it BEFORE the flag:
// `name` is a JSON column full of objects. The child runs with
// BREVIS_NORMALIZE_DATA=true, declares `name_first`/`name_last`, and must be
// refused — not quietly land in two new columns while `name` keeps the
// history.
func TestAFlattenedAwayColumnIsRefused(t *testing.T) {
	const marker = "BREVIS_TEST_FLATRENAME_CHILD"

	if name := os.Getenv(marker); name != "" {
		declared := append(sdk.LandingControlColumns(sdk.LandingOptions{}),
			sdk.Column{Name: "name_first", Type: sdk.TypeString},
			sdk.Column{Name: "name_last", Type: sdk.TypeString})
		err := topg.Table{DSN: dsn(t), Name: name}.
			CheckDestination(context.Background(), declared.Names())
		if err == nil {
			t.Fatal("accepted: `name` would keep the objects already landed " +
				"and nothing would write it again")
		}
		if !strings.Contains(err.Error(), "JSON column") {
			t.Fatalf("this is some other refusal, not the rename one: %v", err)
		}
		for _, want := range []string{"name", "name_first", name} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %q: %v", want, err)
			}
		}
		return
	}

	conn := connect(t)
	ddl := make([]string, 0, 10)
	for _, c := range sdk.LandingControlColumns(sdk.LandingOptions{}) {
		ddl = append(ddl, c.Name+" TEXT")
	}
	ddl = append(ddl, "name JSONB")
	name := table(t, conn, strings.Join(ddl, ","))

	cmd := exec.Command(os.Args[0], "-test.run=TestAFlattenedAwayColumnIsRefused", "-test.v")
	cmd.Env = append(os.Environ(), marker+"="+name,
		"BREVIS_NORMALIZE_DATA=true", "BREVIS_IT_PG_DSN="+dsn(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_NORMALIZE_DATA=true:\n%s", out)
	}

	// And the table was not touched.
	if _, grew := columnTypes(t, conn, name)["name_first"]; grew {
		t.Error("the table grew `name_first`: the check ran too late")
	}
}

// Evolution still works with flattening on: a second batch carrying a new
// nested field adds its flattened columns.
//
// Carried over from S3, where it was covered in principle — the schema
// flattens, so Discovered sees flattened names — and not exercised. "In
// principle" is not a verification.
func TestEvolutionAddsFlattenedColumns(t *testing.T) {
	const marker = "BREVIS_TEST_FLATEVOLVE_CHILD"

	if name := os.Getenv(marker); name != "" {
		declared := sdk.LandingControlColumns(sdk.LandingOptions{})
		to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
			Evolve: sdk.EvolveAdditiveFromPayload}
		opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}
		land := sdk.Landing(name, sdk.LandingKey("id"), sdk.LandingColumns())

		for i, record := range []map[string]any{
			{"id": "A-1", "name": map[string]any{"first": "Coleen"}},
			{"id": "A-2", "name": map[string]any{"first": "Mark", "last": "Regner"}},
		} {
			row, err := land(record)
			if err != nil {
				t.Fatalf("batch %d: %v", i, err)
			}
			if _, err := to.Write(context.Background(), []sdk.Envelope{{Payload: row}}, opt); err != nil {
				t.Fatalf("batch %d: %v", i, err)
			}
		}
		return
	}

	// The child CREATES it, from the declaration, so the control columns get
	// their real types instead of a row of TEXT written by hand here.
	conn := connect(t)
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
	})

	cmd := exec.Command(os.Args[0], "-test.run=TestEvolutionAddsFlattenedColumns", "-test.v")
	cmd.Env = append(os.Environ(), marker+"="+name,
		"BREVIS_NORMALIZE_DATA=true", "BREVIS_IT_PG_DSN="+dsn(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_NORMALIZE_DATA=true:\n%s", out)
	}

	types := columnTypes(t, conn, name)
	for _, want := range []string{"name_first", "name_last"} {
		if _, ok := types[want]; !ok {
			t.Errorf("the table has no %q column: evolution did not see the "+
				"flattened names (%v)", want, columnNames(t, conn, name))
		}
	}
	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}

// The reporter's sequence from issue #42, end to end: a null first, an array
// later, and the array lands.
//
// `asset_asset` was created from a record with `"vehicle_fuel": null`. The
// column became STRING and the next batch carried an array, so the load job
// refused it and every batch holding one went to the dead letter with its
// neighbours. The table had 0 rows. Over 90 days the field was null 786 times
// and an array 62 times.
func TestANullDoesNotTypeAColumnTheArrayCannotFit(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	declared := sdk.LandingControlColumns(sdk.LandingOptions{})
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	opt := sdk.WriteOptions{Schema: declared, Columns: declared.Names()}
	land := sdk.Landing(name, sdk.LandingKey("id"), sdk.LandingColumns())

	// The creating batch, with the field null -- as it was 786 times.
	row, err := land(map[string]any{"id": "A-1", "vehicle_fuel": nil, "price": "10"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.Write(ctx, []sdk.Envelope{{Payload: row}}, opt); err != nil {
		t.Fatalf("the creating batch: %v", err)
	}
	if _, typed := columnTypes(t, conn, name)["vehicle_fuel"]; typed {
		t.Fatalf("a null created the column: it would be text, and the first "+
			"array after it would fail the batch. Columns: %v",
			columnNames(t, conn, name))
	}

	// And the batch that used to be refused -- as it was 62 times.
	row, err = land(map[string]any{
		"id": "A-2", "vehicle_fuel": []any{"gasolina", "etanol"}, "price": "11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.Write(ctx, []sdk.Envelope{{Payload: row}}, opt); err != nil {
		t.Fatalf("the batch with the array -- this is the one that went to the "+
			"dead letter with its neighbours: %v", err)
	}

	if got := columnTypes(t, conn, name)["vehicle_fuel"]; got != "jsonb" {
		t.Errorf("vehicle_fuel is %q, want jsonb: the record that HAS a value "+
			"is the one with a shape", got)
	}

	// Both rows landed, and the first has NULL where it said null.
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
	var fuel *string
	if err := conn.QueryRow(ctx,
		"SELECT vehicle_fuel::text FROM "+name+" WHERE id = 'A-1'").Scan(&fuel); err != nil {
		t.Fatal(err)
	}
	if fuel != nil {
		t.Errorf("the record that said null has %v: dropping a null must write "+
			"the same NULL it meant", *fuel)
	}
}

// The same thing inside ONE batch: the record with the null and the record
// with the array arrive together. [#42]
//
// The dropping happens per FIELD, in landingColumns, and the batch-level
// answer falls out of it: the table is the union of the records, so the array
// still creates the column and the null records simply have no key for it.
// This is the test that the DRIVER agrees -- a row missing a column the batch
// declares has to land NULL, not be refused and not shift the other values
// along.
func TestANullAndAValueForTheSameFieldInOneBatch(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	declared := sdk.LandingControlColumns(sdk.LandingOptions{})
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	land := sdk.Landing(name, sdk.LandingKey("id"), sdk.LandingColumns())

	var batch []sdk.Envelope
	for _, record := range []map[string]any{
		{"id": "A-1", "vehicle_fuel": nil, "price": "10"},
		{"id": "A-2", "vehicle_fuel": []any{"gasolina"}, "price": "11"},
		{"id": "A-3", "vehicle_fuel": nil, "price": "12"},
	} {
		row, err := land(record)
		if err != nil {
			t.Fatal(err)
		}
		batch = append(batch, sdk.Envelope{Payload: row})
	}
	if _, err := to.Write(ctx, batch, sdk.WriteOptions{
		Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("one batch, one null and one array: %v", err)
	}

	if got := columnTypes(t, conn, name)["vehicle_fuel"]; got != "jsonb" {
		t.Errorf("vehicle_fuel is %q, want jsonb", got)
	}
	rows, err := conn.Query(ctx,
		"SELECT id, vehicle_fuel::text, price FROM "+name+" ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]any{}
	for rows.Next() {
		var id, price string
		var fuel *string
		if err := rows.Scan(&id, &fuel, &price); err != nil {
			t.Fatal(err)
		}
		if fuel == nil {
			got[id] = [2]any{nil, price}
			continue
		}
		got[id] = [2]any{*fuel, price}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]any{
		"A-1": {nil, "10"},
		"A-2": {`["gasolina"]`, "11"},
		"A-3": {nil, "12"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the table holds %v, want %v.\n\nA record with no key for a "+
			"column the batch declares lands NULL there -- and its OTHER "+
			"values stay where they belong", got, want)
	}
}

// The reporter's fetcher shape, end to end: a declared vendor field that
// arrives null. [#42, the v0.77.0 regression]
//
// 5 of their 23 fetchers declare a field that is often null. On v0.77.0 the
// landing transformer dropped it from the row and `CheckRow` refused the load
// by name, because a declared column absent from records[0] is a broken chain
// as far as that check knows.
func TestADeclaredColumnThatArrivesNullStillLoads(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	declared := append(
		sdk.LandingControlColumns(sdk.LandingOptions{UniqueID: true, Keyed: true}),
		sdk.Column{Name: "source_key", Type: sdk.TypeString},
		sdk.Column{Name: "Area_Drenagem", Type: sdk.TypeString},
	)
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	land := sdk.Landing(name, sdk.LandingKey("source_key"), sdk.LandingColumns())

	row, err := land(map[string]any{"source_key": "k-1", "Area_Drenagem": nil})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.Write(ctx, []sdk.Envelope{{Payload: row}}, sdk.WriteOptions{
		Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("a declared field that arrived null refused the load: %v", err)
	}

	// And it landed as NULL, which is what the producer said.
	var area *string
	if err := conn.QueryRow(ctx,
		`SELECT "Area_Drenagem" FROM `+name+" WHERE source_key = 'k-1'").Scan(&area); err != nil {
		t.Fatal(err)
	}
	if area != nil {
		t.Errorf("Area_Drenagem is %q, want NULL", *area)
	}
	if got := columnTypes(t, conn, name)["Area_Drenagem"]; got != "text" {
		t.Errorf("Area_Drenagem is %q, want text: a DECLARED column takes its "+
			"type from the declaration, and a null there was never a typing "+
			"problem", got)
	}
}

// An UNDECLARED field that is null throughout contributes no column, and the
// load is not refused for carrying it. [#42]
//
// The other half of the same batch, and the one that cannot be dropped from
// the row without breaking the half above: nothing declares it, so nothing
// creates it, and the nil in the row has to be tolerated rather than refused.
func TestAnUndeclaredNullThroughoutIsNotAnExtra(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	declared := sdk.LandingControlColumns(sdk.LandingOptions{})
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	land := sdk.Landing(name, sdk.LandingKey("id"), sdk.LandingColumns())

	var batch []sdk.Envelope
	for _, record := range []map[string]any{
		{"id": "A-1", "vehicle_fuel": nil, "price": "10"},
		{"id": "A-2", "vehicle_fuel": nil, "price": "11"},
	} {
		row, err := land(record)
		if err != nil {
			t.Fatal(err)
		}
		batch = append(batch, sdk.Envelope{Payload: row})
	}
	if _, err := to.Write(ctx, batch, sdk.WriteOptions{
		Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatalf("a field null in every record refused the load: it declares "+
			"no column, so it is not an extra -- a dropped null and an absent "+
			"field land the same NULL: %v", err)
	}

	if _, typed := columnTypes(t, conn, name)["vehicle_fuel"]; typed {
		t.Errorf("a null created the column: that is #42, and the first array "+
			"after it would fail the batch. Columns: %v", columnNames(t, conn, name))
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}

// A declared column that arrives null is WRITTEN null, not left out.
//
// The difference is invisible on an ordinary INSERT -- a column nobody wrote is
// NULL anyway -- so the table is created here with a DEFAULT, which is what
// separates the two: a column left out of the statement gets the default, and
// one written as NULL gets NULL. [#42]
//
// It is not a curiosity. A column omitted because its value was nil is a
// producer's "this field is now empty" being discarded, and the DEFAULT here
// stands in for every destination-side rule that fills a gap -- a default, a
// generated column, a trigger.
//
// A mutation that dropped a nil-throughout field even when the table HAS the
// column survived every other test in this file, because every other test was
// looking at a column that would have been NULL either way.
func TestADeclaredNullIsWrittenAndNotOmitted(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	declared := append(
		sdk.LandingControlColumns(sdk.LandingOptions{UniqueID: true, Keyed: true}),
		sdk.Column{Name: "source_key", Type: sdk.TypeString},
		sdk.Column{Name: "Area_Drenagem", Type: sdk.TypeString},
	)
	// Ours, not the driver's, because the DEFAULT is the measurement.
	if _, err := conn.Exec(ctx, `CREATE TABLE `+name+` (
		brevis_ingestion_id text PRIMARY KEY,
		brevis_record_key text,
		brevis_operation text,
		brevis_received_at timestamptz,
		brevis_loaded_at timestamptz DEFAULT now(),
		brevis_stream text,
		brevis_gateway text,
		brevis_received_bytes bigint,
		source_key text,
		"Area_Drenagem" text DEFAULT 'the default, not the null'
	)`); err != nil {
		t.Fatal(err)
	}

	to := topg.Table{DSN: dsn(t), Name: name}
	land := sdk.Landing(name, sdk.LandingKey("source_key"), sdk.LandingColumns())
	row, err := land(map[string]any{"source_key": "k-1", "Area_Drenagem": nil})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.Write(ctx, []sdk.Envelope{{Payload: row}}, sdk.WriteOptions{
		Schema: declared, Columns: declared.Names()}); err != nil {
		t.Fatal(err)
	}

	var area *string
	if err := conn.QueryRow(ctx,
		`SELECT "Area_Drenagem" FROM `+name).Scan(&area); err != nil {
		t.Fatal(err)
	}
	if area != nil {
		t.Errorf("Area_Drenagem is %q: the column was left out of the "+
			"statement and the DEFAULT filled it, so the producer's null was "+
			"discarded. A declared column that arrives empty has to be "+
			"WRITTEN empty", *area)
	}
}

// The gateway's config shape, end to end through a real driver: a Schema, NO
// Columns, and a field null in every record. [#42]
//
// This is the shape no test in this repository had, and three releases in a
// row shipped a bug in it. The SDK's own tests all declare Columns, because
// the facade fills it from the Schema -- only a driver used directly, which is
// what the gateway does, arrives with Columns empty.
//
// It was caught the first two times by a consumer and the third time by
// running the PUBLISHED artifact. Running the published artifact is worth
// keeping; it should not be the only place this shape is exercised.
func TestTheGatewaysShapeLoadsEndToEnd(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+name) })

	declared := sdk.LandingControlColumns(sdk.LandingOptions{UniqueID: true, Keyed: true})
	to := topg.Table{DSN: dsn(t), Name: name, CreateTable: true,
		Evolve: sdk.EvolveAdditiveFromPayload}
	land := sdk.Landing(name, sdk.LandingKey("id"), sdk.LandingColumns())

	var batch []sdk.Envelope
	for _, id := range []string{"A-1", "A-2"} {
		row, err := land(map[string]any{"id": id, "inactive_at": nil})
		if err != nil {
			t.Fatal(err)
		}
		batch = append(batch, sdk.Envelope{Payload: row})
	}

	// Schema and NO Columns -- exactly what the gateway's sink builds.
	if _, err := to.Write(ctx, batch, sdk.WriteOptions{Schema: declared}); err != nil {
		t.Fatalf("the gateway's own shape was refused:\n\n%v", err)
	}

	// The column the batch brought exists; the one that was null in every
	// record does not.
	types := columnTypes(t, conn, name)
	if _, ok := types["id"]; !ok {
		t.Errorf("the batch's own column was not created: %v", columnNames(t, conn, name))
	}
	if _, ok := types["inactive_at"]; ok {
		t.Error("a null created inactive_at: the first value after it would " +
			"have to fit a type nobody chose")
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}
