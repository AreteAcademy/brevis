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
		`[{"table_name":%q,"occurred_at":"2026-09-25T10:00:00Z","amount":10,"who":"ana"}]`, a),
		http.StatusAccepted)
	postKeyed(t, ts.URL+"/v1/tables", fmt.Sprintf(
		`[{"table_name":%q,"occurred_at":"2026-09-25T10:00:00Z","level":"warn"}]`, b),
		http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	// Both tables exist, with the four columns and nothing else.
	for _, table := range []string{a, b} {
		cols := query(t, dsn, fmt.Sprintf(
			`SELECT column_name || ' ' || data_type FROM information_schema.columns
			 WHERE table_name = '%s' ORDER BY column_name`, table))
		want := "data jsonb,ingested_at timestamp with time zone," +
			"ingestion_id text,occurred_at timestamp with time zone"
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
	if data["amount"] != float64(10) || data["who"] != "ana" || data["table_name"] != a {
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
		`[{"table_name":%q,"idempotency_key":"ord-1","occurred_at":"2026-09-25T10:00:00Z","status":"paid"}]`,
		table)
	postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
	postKeyed(t, ts.URL+"/v1/tables", body, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The table is created without a unique index, so `merge` would refuse.
	// `append` is what this stream uses, and the proof is that both rows carry
	// the SAME id — which is what lets anything downstream deduplicate.
	ids := query(t, dsn, `SELECT DISTINCT ingestion_id FROM `+table)
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
		`[{"table_name":%q,"occurred_at":"2026-09-25T10:00:00Z"},`+
			`{"table_name":"x\";DROP TABLE y;--","occurred_at":"2026-09-25T10:00:00Z"}]`,
		good))
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

func autoTableGateway(t *testing.T, dsn, dead string) *gateway.Server {
	t.Helper()
	if dead == "" {
		dead = t.TempDir()
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
    identity: {provider: p, entity: e, source_key: table_name, record_ts: occurred_at}
    buffer: {flush: {records: 500, every: 1h}}
    retry: {attempts: 1}
    sink:
      type: auto_table
      table_from: table_name
      naming: {pattern: '^gwauto_[a-z0-9_]+$'}
      into: {type: postgres, dsn_from: PG_DSN, write: append}
    dead_letter: {type: files, path: %s/}
`, dead)

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
