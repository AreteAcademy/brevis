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

func conectar(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("conectando: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// table creates a throwaway table and returns its name.
func tabela(t *testing.T, conn *pgx.Conn, ddl string) string {
	t.Helper()
	nome := fmt.Sprintf("t_%d", time.Now().UnixNano())
	if _, err := conn.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s (%s)", nome, ddl)); err != nil {
		t.Fatalf("criando %s: %v", nome, err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+nome)
	})
	return nome
}

func env(payload map[string]any) sdk.Envelope { return sdk.Envelope{Payload: payload} }

const colunasPadrao = `
	ingestion_id TEXT NOT NULL,
	ingestion_loaded_at TIMESTAMPTZ NOT NULL,
	provider TEXT,
	source_key TEXT,
	valor NUMERIC(18,2)`

func loteDeTeste(n int) []sdk.Envelope {
	out := make([]sdk.Envelope, n)
	agora := time.Now().UTC().Format(time.RFC3339)
	for i := range out {
		out[i] = env(map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%04d", i),
			"ingestion_loaded_at": agora,
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
	conn := conectar(t)
	nome := tabela(t, conn, colunasPadrao)

	res, err := topg.Table{DSN: dsn(t), Name: nome}.Write(
		context.Background(), loteDeTeste(3), sdk.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.RowsLoaded != 3 {
		t.Errorf("RowsLoaded = %d, esperado 3", res.RowsLoaded)
	}

	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+nome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("o servidor tem %d rows, esperado 3", n)
	}
}

// TestIntegrationTheTablesOrderIsWhatCounts proves, end to end, that every value
// lands in the right column when the table's order is not the record's.
func TestIntegrationTheTablesOrderIsWhatCounts(t *testing.T) {
	conn := conectar(t)
	// The table's order is deliberately different from the order the record
	// costuma ser escrito.
	nome := tabela(t, conn, `
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
	if _, err := (topg.Table{DSN: dsn(t), Name: nome}).Write(
		context.Background(), lote, sdk.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var id, prov, valor string
	err := conn.QueryRow(context.Background(),
		"SELECT ingestion_id, provider, valor::text FROM "+nome).Scan(&id, &prov, &valor)
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc" || prov != "acme" || valor != "99.90" {
		t.Errorf("valores trocados de coluna: id=%q provider=%q valor=%q", id, prov, valor)
	}
}

// TestIntegrationDedupLoadsTheSameBatchTwice is BigQuery's test ported, and it
// is phase 2's done criterion.
func TestIntegrationDedupLoadsTheSameBatchTwice(t *testing.T) {
	conn := conectar(t)
	nome := tabela(t, conn, colunasPadrao)
	if _, err := conn.Exec(context.Background(),
		fmt.Sprintf("CREATE UNIQUE INDEX ON %s (ingestion_id)", nome)); err != nil {
		t.Fatal(err)
	}

	destino := topg.Table{DSN: dsn(t), Name: nome}
	lote := loteDeTeste(5)
	opt := sdk.WriteOptions{Dedup: sdk.DedupMerge}

	primeira, err := destino.Write(context.Background(), lote, opt)
	if err != nil {
		t.Fatalf("primeira carga: %v", err)
	}
	if primeira.RowsLoaded != 5 || primeira.RowsIgnored != 0 {
		t.Errorf("primeira: %d carregadas, %d ignoradas", primeira.RowsLoaded, primeira.RowsIgnored)
	}

	segunda, err := destino.Write(context.Background(), lote, opt)
	if err != nil {
		t.Fatalf("segunda carga: %v", err)
	}
	if segunda.RowsLoaded != 0 || segunda.RowsIgnored != 5 {
		t.Errorf("segunda: %d carregadas, %d ignoradas -- esperava 0 e 5",
			segunda.RowsLoaded, segunda.RowsIgnored)
	}

	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+nome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("a tabela tem %d rows depois de duas cargas do mesmo lote", n)
	}
}

// TestIntegrationDedupWithoutAnIndexRefuses: with no unique index, ON CONFLICT
// has nothing to match and every run would insert duplicates.
func TestIntegrationDedupWithoutAnIndexRefuses(t *testing.T) {
	conn := conectar(t)
	nome := tabela(t, conn, colunasPadrao)

	_, err := topg.Table{DSN: dsn(t), Name: nome}.Write(
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
	conn := conectar(t)
	nome := tabela(t, conn, colunasPadrao)

	lote := []sdk.Envelope{env(map[string]any{
		"ingestion_id":        "x",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"coluna_inexistente":  1,
	})}
	_, err := topg.Table{DSN: dsn(t), Name: nome}.Write(context.Background(), lote, sdk.WriteOptions{})
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
	conn := conectar(t)
	nome := tabela(t, conn, "i INT, texto TEXT")
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		`INSERT INTO %s SELECT g, repeat('x', 500) FROM generate_series(1, 50000) g`, nome)); err != nil {
		t.Fatal(err)
	}

	fonte := frompg.Query{DSN: dsn(t), SQL: "SELECT i, texto FROM " + nome + " ORDER BY i"}
	seq, err := fonte.Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	inicio := time.Now()
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
	if d := time.Since(inicio); d > 2*time.Second {
		t.Errorf("a primeira linha levou %s; o driver parece bufferizar", d)
	}
}

// TestIntegrationTheTypesComeFromTheServer proves §3.1's table against a real
// Postgres, and not against the values we built ourselves.
func TestIntegrationTheTypesComeFromTheServer(t *testing.T) {
	fonte := frompg.Query{DSN: dsn(t), SQL: `
		SELECT
			123456789012345678.99::numeric   AS numerico,
			'2026-09-05'::date               AS data,
			'2026-09-05 12:30:00+00'::timestamptz AS instante,
			'\xdeadbeef'::bytea              AS bytes,
			'{"a":[1,2]}'::jsonb             AS documento,
			'178d0b49-dece-5738-b8eb-f5cae2221aea'::uuid AS identificador,
			NULL::text                       AS vazio,
			ARRAY[1,2,3]                     AS numeros`}

	seq, err := fonte.Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var linha map[string]any
	for e, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		linha = e.Payload.(map[string]any)
	}
	if linha == nil {
		t.Fatal("nenhuma linha")
	}

	esperado := map[string]any{
		"numerico":      "123456789012345678.99",
		"data":          "2026-09-05",
		"instante":      "2026-09-05T12:30:00Z",
		"identificador": "178d0b49-dece-5738-b8eb-f5cae2221aea",
		"vazio":         nil,
	}
	for campo, quero := range esperado {
		if got := linha[campo]; got != quero {
			t.Errorf("%s = %#v, esperado %#v", campo, got, quero)
		}
	}
	if _, ok := linha["documento"].(map[string]any); !ok {
		t.Errorf("documento = %#v; JSONB devia chegar aninhado", linha["documento"])
	}
	if b, ok := linha["bytes"].([]byte); !ok || len(b) != 4 {
		t.Errorf("bytes = %#v; BYTEA devia chegar como []byte", linha["bytes"])
	}
}

// TestIntegrationPostgresToPostgres is phase 2's done criterion: the whole
// pipeline, with dedup proven by loading the same batch twice.
func TestIntegrationPostgresToPostgres(t *testing.T) {
	conn := conectar(t)

	origem := tabela(t, conn, "id INT, nome TEXT, valor NUMERIC(18,2), atualizado_em TIMESTAMPTZ")
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(
		`INSERT INTO %s SELECT g, 'registro ' || g, (g * 1.5)::numeric, now() FROM generate_series(1, 100) g`,
		origem)); err != nil {
		t.Fatal(err)
	}

	destino := tabela(t, conn, `
		ingestion_id TEXT NOT NULL,
		ingestion_loaded_at TIMESTAMPTZ NOT NULL,
		provider TEXT NOT NULL,
		entity TEXT NOT NULL,
		source_key TEXT NOT NULL,
		record_ts TEXT NOT NULL,
		nome TEXT,
		valor NUMERIC(18,2)`)
	if _, err := conn.Exec(context.Background(),
		fmt.Sprintf("CREATE UNIQUE INDEX ON %s (ingestion_id)", destino)); err != nil {
		t.Fatal(err)
	}

	rodar := func() *sdk.Result {
		t.Helper()
		dados, err := sdk.Extract(context.Background(), sdk.Source{
			From: frompg.Query{
				DSN: dsn(t),
				SQL: "SELECT id, nome, valor, atualizado_em FROM " + origem + " ORDER BY id",
			},
		})
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		dados = sdk.Transform(dados,
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
		res, err := sdk.Load(context.Background(), dados, sdk.Target{
			To: topg.Table{DSN: dsn(t), Name: destino},
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

	primeira := rodar()
	if primeira.Rows != 100 {
		t.Errorf("primeira carga: %d rows, esperado 100", primeira.Rows)
	}

	segunda := rodar()
	if segunda.Rows != 0 || segunda.Ignored != 100 {
		t.Errorf("segunda carga: %d carregadas e %d ignoradas -- o mesmo lote devia ser todo dedup",
			segunda.Rows, segunda.Ignored)
	}

	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+destino).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 100 {
		t.Errorf("o destino tem %d rows depois de duas execuções idênticas", n)
	}

	// And the precision survived the whole crossing: NUMERIC in Postgres, a
	// string in the record, NUMERIC back again.
	var valor string
	if err := conn.QueryRow(context.Background(),
		"SELECT valor::text FROM "+destino+" WHERE source_key = '2'").Scan(&valor); err != nil {
		t.Fatal(err)
	}
	if valor != "3.00" {
		t.Errorf("valor = %q, esperado \"3.00\"", valor)
	}
}
