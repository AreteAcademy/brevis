package postgres_test

import (
	"context"
	"fmt"
	"os"
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

// TestIntegrationARowActuallyGoesIn is §5.1 of the plan: the in-memory tests
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
	// The table's order is deliberately different from the order the record
	// costuma ser escrito.
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

// TestIntegrationTheReadIsStreamed is §5.3: a test that FAILS if the driver
// bufferizar em vez de fazer streaming.
//
// It consumes one row and stops. If the driver built the whole list before
// devolver, ele teria lido as 50 mil -- e o tempo denunciaria.
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
			// to
			// produzir o mesmo id.
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
