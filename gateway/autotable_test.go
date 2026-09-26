package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

// One route, N tables, nothing declared — end to end against a real Postgres.
//
// Postgres and not BigQuery deliberately: BigQuery has no emulator, so a test
// against it would prove the config and nothing else. The four columns are the
// SAME everywhere — the SDK's DDL generator turns TypeJSON into JSONB here,
// JSON on BigQuery and MySQL, SUPER on Redshift — so what this proves about the
// shape holds for all of them.
func TestIntegrationAutoTableCreatesAndRoutes(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	a, b := uniqueTable(t, dsn), uniqueTable(t, dsn)

	srv := autoTableGateway(t, dsn, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Two producers, two tables, one route, and neither table exists.
	postKeyed(t, ts.URL+"/v1/tables", fmt.Sprintf(
		`[{"table_name":%q,"data":{"id":"A-1","amount":10,"who":"ana"}}]`, a),
		http.StatusAccepted)
	postKeyed(t, ts.URL+"/v1/tables", fmt.Sprintf(
		`[{"table_name":%q,"data":{"id":"L-1","level":"warn"}}]`, b),
		http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	// Both tables exist, with the fixed columns and nothing else.
	for _, table := range []string{a, b} {
		cols := query(t, dsn, fmt.Sprintf(
			`SELECT column_name || ' ' || data_type FROM information_schema.columns
			 WHERE table_name = '%s' ORDER BY column_name`, table))
		want := "brevis_gateway text,brevis_ingestion_id text," +
			"brevis_loaded_at timestamp with time zone,brevis_operation text," +
			"brevis_received_at timestamp with time zone," +
			"brevis_received_bytes bigint,brevis_record_key text," +
			"brevis_stream text,data jsonb"
		if got := strings.Join(cols, ","); got != want {
			t.Errorf("%s has columns %q, want %q", table, got, want)
		}
	}

	// The producer's whole event is in `data`, including the field that named
	// the table.
	rows := query(t, dsn, `SELECT data::text FROM `+a)
	if len(rows) != 1 {
		t.Fatalf("%s holds %d rows", a, len(rows))
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(rows[0]), &data); err != nil {
		t.Fatal(err)
	}
	if data["amount"] != float64(10) || data["who"] != "ana" || data["id"] != "A-1" {
		t.Errorf("the event did not survive whole: %v", data)
	}

	// And the other table got the other event, not this one.
	if got := query(t, dsn, `SELECT data->>'level' FROM `+b); len(got) != 1 || got[0] != "warn" {
		t.Errorf("%s holds %v", b, got)
	}
}

// A retried POST is one row, because the identity is a frozen function of the
// event — which is the property the whole product rests on and the one most at
// risk when nothing is declared.
func TestIntegrationAutoTableAbsorbsARetry(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	table := uniqueTable(t, dsn)

	srv := autoTableGateway(t, dsn, "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := fmt.Sprintf(
		`[{"table_name":%q,"data":{"id":"ord-1","status":"paid"}}]`, table)
	postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
	postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The table is created without a unique index, so `merge` would refuse.
	// `append` is what this stream uses, and the proof is that both rows carry
	// the SAME id — which is what lets anything downstream deduplicate.
	ids := query(t, dsn, `SELECT DISTINCT brevis_ingestion_id FROM `+table)
	if len(ids) != 1 {
		t.Errorf("a retried POST produced %d distinct ids: %v", len(ids), ids)
	}
}

// A name a producer may not use is refused PER EVENT, told to the producer, and
// the rest of the request still lands.
//
// The batch is where this would go wrong. Refusing the name at write time would
// fail the whole batch -- one malformed event from one producer burying the
// events of every other producer in the same flush window, none of them told.
// The first version of this did exactly that, and this test is what caught it:
// the good table was never created.
func TestIntegrationAutoTableRefusesANameOutsideTheRules(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	good := uniqueTable(t, dsn)

	dead := t.TempDir()
	srv := autoTableGateway(t, dsn, dead)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// One request carrying both: a good event and a name that would become DDL
	// if anything here quoted badly.
	answer := postBody(t, ts.URL+"/v1/tables", fmt.Sprintf(
		`[{"table_name":%q,"data":{"id":"ok-1"}},`+
			`{"table_name":"x\";DROP TABLE y;--","data":{"id":"bad-1"}}]`, good))
	_ = srv.Close(context.Background())

	// The producer is told, in the response, at the moment they can fix it.
	if answer["accepted"] != float64(1) {
		t.Errorf("accepted is %v, want 1: %v", answer["accepted"], answer)
	}
	rejected, _ := answer["rejected"].([]any)
	if len(rejected) != 1 || !strings.Contains(fmt.Sprint(rejected[0]), "does not match") {
		t.Errorf("the response does not say the name was refused: %v", answer)
	}

	// And the good event landed anyway, which is the whole point.
	if got := query(t, dsn, `SELECT count(*)::text FROM `+good); got[0] != "1" {
		t.Errorf("the good event did not land: %v", got)
	}
	// Nothing was buried: a refused event is not a refused batch.
	if entries, _ := os.ReadDir(dead); len(entries) != 0 {
		t.Errorf("a per-event refusal reached the dead letter: %d files", len(entries))
	}
}

// postBody posts and returns the decoded answer.
func postBody(t *testing.T, url, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// postKeyed posts with the bearer key, because `auto_table` refuses to load on
// an endpoint with no auth at all -- a producer that can name a table can
// create one, so it needs authentication even where an ordinary stream would
// not.
func postKeyed(t *testing.T, url, body string, want int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		t.Fatalf("POST returned %d, want %d", resp.StatusCode, want)
	}
}

// `merge` works on a table auto_table created, which it could not before.
//
// DedupMerge needs a unique index on ingestion_id and Postgres refuses without
// one. A table created by the router had no constraint, so the create
// succeeded and every load refused -- found by running the published image:
// two tables with the right four columns and zero rows in them.
//
// The constraint has to follow the write mode in BOTH directions, so this
// checks both: `merge` absorbs the redelivery, and `append` keeps it.
func TestIntegrationAutoTableMergeAbsorbsARedelivery(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	for _, c := range []struct {
		write string
		want  string
	}{
		{"merge", "1"},  // the redelivery is ignored
		{"append", "2"}, // every delivery lands, which is what append promises
	} {
		t.Run(c.write, func(t *testing.T) {
			t.Setenv("PG_DSN", dsn)
			table := uniqueTable(t, dsn)

			srv := autoTableGateway(t, dsn, "", c.write)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			body := fmt.Sprintf(
				`[{"table_name":%q,"data":{"id":"o-1","amount":10}}]`, table)
			postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
			postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
			if err := srv.Close(context.Background()); err != nil {
				t.Fatalf("draining: %v", err)
			}

			if got := query(t, dsn, `SELECT count(*)::text FROM `+table); got[0] != c.want {
				t.Errorf("%s left %s rows, want %s", c.write, got[0], c.want)
			}

			// And the constraint is there for merge and absent for append: a
			// unique index on an append table would reject the second delivery,
			// which is the opposite of what it promises.
			idx := query(t, dsn, fmt.Sprintf(
				`SELECT count(*)::text FROM pg_indexes WHERE tablename = '%s'`, table))
			wantIdx := "0"
			if c.write == "merge" {
				wantIdx = "1"
			}
			if idx[0] != wantIdx {
				t.Errorf("%s created %s indexes, want %s", c.write, idx[0], wantIdx)
			}
		})
	}
}

func autoTableGateway(t *testing.T, dsn, dead string, write ...string) *gateway.Server {
	t.Helper()
	if dead == "" {
		dead = t.TempDir()
	}
	mode, shape, records, meta := "append", "document", "500", "{type: memory}"
	if len(write) > 0 {
		mode = write[0]
	}
	if len(write) > 1 {
		shape = write[1]
	}
	if len(write) > 2 {
		records = write[2]
	}
	if len(write) > 3 {
		meta = "{type: " + write[3] + ", addr_from: REDIS_ADDR}"
	}
	t.Setenv("GW_KEYS", "k")
	yaml := fmt.Sprintf(`
name: auto
listen:
  auth: {type: bearer, keys_from: GW_KEYS}
streams:
  - name: tables
    path: /v1/tables
    format: array
    identity: {provider: p, entity: e, source_key: table_name, record_ts: table_name}
    buffer: {flush: {records: %s, every: 1h}}
    retry: {attempts: 4, backoff: 500ms, max_backoff: 2s}
    sink:
      type: auto_table
      naming: {pattern: '^gwauto_[a-z0-9_]+$'}
      metastore: %s
      shape: %s
      into: {type: postgres, dsn_from: PG_DSN, write: %s}
    dead_letter: {type: files, path: %s/}
`, records, meta, shape, mode, dead)

	file := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(file)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, nil, everything()...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// `shape: columns` — each field of the record gets a column, scalars STRING and
// objects JSON, with no inference anywhere.
//
// This is the shape the contract was designed around, and it is the expensive
// one: with no overflow column, a field the table does not have cannot land.
// What it buys is a flat table nobody has to unpack.
func TestIntegrationAutoTableColumnsShape(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	table := uniqueTable(t, dsn)

	srv := autoTableGateway(t, dsn, "", "merge", "columns")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postKeyed(t, ts.URL+"/v1/tables", fmt.Sprintf(`[{
		"table_name": %q,
		"unique_key": "id",
		"operation":  "INSERT",
		"data": {
			"id": "A-3",
			"total": 150,
			"customer": {"id": 7, "uf": "SP"},
			"items": [{"sku": "X", "qty": 2}]
		}}]`, table), http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	// The table the contract describes: the fixed columns plus the record's
	// own, scalars TEXT and objects JSONB.
	cols := query(t, dsn, fmt.Sprintf(
		`SELECT column_name||' '||data_type FROM information_schema.columns
		 WHERE table_name='%s' ORDER BY column_name`, table))
	want := "brevis_gateway text,brevis_ingestion_id text," +
		"brevis_loaded_at timestamp with time zone,brevis_operation text," +
		"brevis_received_at timestamp with time zone," +
		"brevis_received_bytes bigint,brevis_record_key text," +
		"brevis_stream text,customer jsonb,id text,items jsonb,total text"
	if got := strings.Join(cols, ","); got != want {
		t.Errorf("columns are\n  %q\nwant\n  %q", got, want)
	}

	// The values, including the number that stayed a string because nothing
	// here infers a type.
	row := query(t, dsn, `SELECT id||'|'||total||'|'||(customer->>'uf')||'|'||
		(items->0->>'sku')||'|'||brevis_record_key||'|'||brevis_operation FROM `+table)
	if len(row) != 1 || row[0] != "A-3|150|SP|X|A-3|INSERT" {
		t.Errorf("the row is %v", row)
	}

	// brevis_loaded_at was stamped by the DESTINATION, not sent by us — which
	// is what makes the gap between it and brevis_received_at the real
	// end-to-end latency.
	if got := query(t, dsn, `SELECT (brevis_loaded_at IS NOT NULL)::text FROM `+table); got[0] != "true" {
		t.Error("brevis_loaded_at is null, so the DEFAULT did not fire")
	}
}

// The scenario, end to end: a field appears and the table grows a column.
//
// Three events, each with its own key. The second and third carry a field the
// first did not, so the table has to evolve between the first batch and the
// second — and the third must NOT evolve it again, because by then the column
// is there.
func TestIntegrationAutoTableEvolvesWhenAFieldAppears(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	table := uniqueTable(t, dsn)

	srv := autoTableGateway(t, dsn, "", "merge", "columns", "1")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// One event per batch (flush.records is 1 for this stream), and each is
	// waited for: the second has to meet a table that was created WITHOUT the
	// column. One batch holding all three would declare the union and there
	// would be nothing to evolve.
	columnsNow := func() string {
		return strings.Join(query(t, dsn, fmt.Sprintf(
			`SELECT column_name FROM information_schema.columns
			 WHERE table_name='%s' AND column_name NOT LIKE 'brevis_%%' ORDER BY column_name`,
			table)), ",")
	}
	post := func(body, want string) {
		t.Helper()
		postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
		for i := 0; i < 100; i++ {
			if columnsNow() == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("after %s the columns are %q, want %q", body, columnsNow(), want)
	}

	post(fmt.Sprintf(`[{"table_name":%q,"data":{"id":"A-1","total":150}}]`, table), "id,total")

	// The field appears, and the table grows a column.
	post(fmt.Sprintf(`[{"table_name":%q,"data":{"id":"A-2","total":150,"novo_campo":"Novo valor 1"}}]`, table),
		"id,novo_campo,total")

	// And again, with a different value: nothing left to evolve, and it lands.
	post(fmt.Sprintf(`[{"table_name":%q,"data":{"id":"A-3","total":150,"novo_campo":"Novo valor 2"}}]`, table),
		"id,novo_campo,total")
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows := query(t, dsn, `SELECT id||'|'||coalesce(novo_campo,'(null)') FROM `+table+` ORDER BY id`)
	want := "A-1|(null),A-2|Novo valor 1,A-3|Novo valor 2"
	if got := strings.Join(rows, ","); got != want {
		t.Errorf("the rows are %q, want %q", got, want)
	}

	// The first event's row keeps NULL there, which is what an added column
	// does and why only additive is allowed: nothing already written moves.
	if got := query(t, dsn, `SELECT count(*)::text FROM `+table+` WHERE novo_campo IS NULL`); got[0] != "1" {
		t.Errorf("%s rows are null, want 1", got[0])
	}
}
