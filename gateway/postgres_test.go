package gateway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/jackc/pgx/v5"
)

// The two write modes against a real Postgres, asserted by reading the TABLE
// back.
//
// Reading it back is the point, and it is the same reason the Pub/Sub
// integration test subscribes: everything up to the write can be asserted
// against a fake, and what actually landed is the only thing that says the
// semantics held. `append` and `merge` differ by exactly one thing -- what a
// redelivery does -- and that difference is invisible anywhere but in the rows.
func TestIntegrationPostgresWriteModes(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}

	for _, c := range []struct {
		mode    string
		unique  bool
		want    int
		wantSeq string
	}{
		// A log. Both deliveries land, and that is the mode's whole promise:
		// the table records arrivals, not events.
		{mode: "append", unique: false, want: 2, wantSeq: "first,second"},

		// A table. The second delivery carries the same ingestion_id, so
		// ON CONFLICT DO NOTHING drops it -- and the row keeps the FIRST
		// value. That is the half of this that is easy to get backwards.
		{mode: "merge", unique: true, want: 1, wantSeq: "first"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			table := newTable(t, dsn, c.unique)
			t.Setenv("PG_DSN", dsn)

			srv := postgresGateway(t, table, c.mode)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			// The same event twice, differing only in a field that is not part
			// of the identity: same event_id, same occurred_at, so the same
			// ingestion_id -- which is what makes this a REDELIVERY and not two
			// events.
			post(t, ts.URL+"/v1/clicks", event("e-1", "first"), http.StatusAccepted)
			post(t, ts.URL+"/v1/clicks", event("e-1", "second"), http.StatusAccepted)
			if err := srv.Close(context.Background()); err != nil {
				t.Fatalf("draining: %v", err)
			}

			rows := query(t, dsn, `SELECT host FROM `+table+` ORDER BY host`)
			if len(rows) != c.want {
				t.Fatalf("%s left %d rows, want %d: %v", c.mode, len(rows), c.want, rows)
			}
			if got := strings.Join(rows, ","); got != c.wantSeq {
				t.Errorf("%s left %q, want %q", c.mode, got, c.wantSeq)
			}

			// Both modes stamp the id, and it is the same one for both
			// deliveries -- which is what a merge matches on and what anything
			// downstream deduplicates by.
			ids := query(t, dsn, `SELECT DISTINCT ingestion_id FROM `+table)
			if len(ids) != 1 || len(ids[0]) != 36 {
				t.Errorf("the ingestion_id is not one frozen id: %v", ids)
			}
		})
	}
}

// merge onto a table with no unique index is refused, and the refusal reaches
// the operator rather than the data.
//
// This is the failure the mode exists to prevent: without the index there is
// nothing for ON CONFLICT to match, so every redelivery would insert a
// duplicate into a table whose owner asked for the opposite. The driver refuses;
// the gateway retries, gives up, and the events land in the dead letter with the
// reason ON them -- so nobody loses events while somebody creates the index.
func TestIntegrationPostgresMergeRefusesATableWithoutTheIndex(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	table := newTable(t, dsn, false)
	t.Setenv("PG_DSN", dsn)

	dead := t.TempDir()
	srv := postgresGateway(t, table, "merge", dead)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("e-1", "first"), http.StatusAccepted)
	_ = srv.Close(context.Background())

	if rows := query(t, dsn, `SELECT host FROM `+table); len(rows) != 0 {
		t.Errorf("it wrote anyway: %v", rows)
	}
	buried := buriedReason(t, dead)
	if !strings.Contains(buried, "unique index") {
		t.Errorf("the dead letter does not carry the reason: %s", buried)
	}
	// The command that fixes it travels with the record, because whoever opens
	// this file has the events and not the log.
	if !strings.Contains(buried, "CREATE UNIQUE INDEX") {
		t.Errorf("the dead letter does not say how to fix it: %s", buried)
	}
}

// A batch whose events do not all carry the same fields still lands.
//
// This is why the sink declares no Columns. A pipeline knows its row's shape; a
// gateway's batch is whatever N clients posted in one flush window, and an
// event that omits an optional field is written as NULL rather than failing the
// batch that its neighbour is in.
func TestIntegrationPostgresABatchOfDifferentShapesLands(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	table := newTable(t, dsn, false)
	t.Setenv("PG_DSN", dsn)

	srv := postgresGateway(t, table, "append")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Two events in one batch, and the SHORT one first. The order matters:
	// a declared column list is checked against the first record of the batch,
	// so declaring one here would refuse this batch for missing `host` -- while
	// the same two events in the other order would sail through. A rule that
	// depends on which client posted first is not a rule.
	post(t, ts.URL+"/v1/clicks",
		`{"event_id":"e-2","occurred_at":"2026-09-24T10:00:00Z"}`, http.StatusAccepted)
	post(t, ts.URL+"/v1/clicks", event("e-1", "acme.example.com"), http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	rows := query(t, dsn,
		`SELECT event_id || '=' || coalesce(host, 'NULL') FROM `+table+` ORDER BY event_id`)
	want := "e-1=acme.example.com,e-2=NULL"
	if got := strings.Join(rows, ","); got != want {
		t.Errorf("the table holds %q, want %q", got, want)
	}
}

// A field the table does not have is refused BEFORE the server is touched, and
// the refusal names the column.
//
// The alternative is Postgres failing in the middle of a COPY with `column "x"
// of relation "y" does not exist`, after the whole batch has been streamed and
// without saying what to do about it.
func TestIntegrationPostgresAnUnknownColumnIsRefusedWithItsName(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	table := newTable(t, dsn, false)
	t.Setenv("PG_DSN", dsn)

	dead := t.TempDir()
	srv := postgresGateway(t, table, "append", dead)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks",
		`{"event_id":"e-1","occurred_at":"2026-09-24T10:00:00Z","surprise":1}`,
		http.StatusAccepted)
	_ = srv.Close(context.Background())

	if rows := query(t, dsn, `SELECT host FROM `+table); len(rows) != 0 {
		t.Errorf("it wrote anyway: %v", rows)
	}
	buried := buriedReason(t, dead)
	if !strings.Contains(buried, "surprise") {
		t.Errorf("the refusal does not name the column: %s", buried)
	}
}

// An empty connection variable is refused at construction, not on the first
// event.
//
// `dsn_from` names a variable; an unset one means the deployment forgot the
// secret. Finding that out at startup is a pod that will not go ready; finding
// it out on the first request is a pod that went ready and drops events.
func TestAPostgresSinkWithNoConnectionStringIsRefusedAtStartup(t *testing.T) {
	t.Setenv("PG_DSN", "")
	_, err := gateway.New(postgresConfig(t, "landing.clicks", "append", t.TempDir()), nil, everything()...)
	if err == nil {
		t.Fatal("it started")
	}
	if !strings.Contains(err.Error(), "PG_DSN is empty") {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
}

// buriedReason is the reason stamped onto the first record in the dead letter.
// It reuses the reader the dead-letter tests already have, so there is one idea
// of what that folder holds.
func buriedReason(t *testing.T, dir string) string {
	t.Helper()
	rows := readDeadLetter(t, dir)
	if len(rows) == 0 {
		t.Fatal("the dead letter is empty")
	}
	reason, _ := rows[0]["_dead_letter_reason"].(string)
	return reason
}

func postgresGateway(t *testing.T, table, mode string, dead ...string) *gateway.Server {
	t.Helper()
	path := t.TempDir()
	if len(dead) > 0 {
		path = dead[0]
	}
	srv, err := gateway.New(postgresConfig(t, table, mode, path), nil, everything()...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func postgresConfig(t *testing.T, table, mode, dead string) *gateway.Config {
	t.Helper()
	yaml := fmt.Sprintf(`
name: pg_gateway
listen:
  addr: :0
streams:
  - name: clicks
    path: /v1/clicks
    format: json
    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at
    buffer:
      flush:
        records: 500
        every: 1h
    retry:
      attempts: 1
    sink:
      type: postgres
      dsn_from: PG_DSN
      table: %s
      write: %s
    dead_letter:
      type: files
      path: %s
`, table, mode, dead)

	file := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(file)
	if err != nil {
		t.Fatalf("loading the config: %v", err)
	}
	return cfg
}

func event(id, host string) string {
	return fmt.Sprintf(
		`{"event_id":%q,"occurred_at":"2026-09-24T10:00:00Z","host":%q}`, id, host)
}

// newTable builds a table per subtest, so one test's rows are never another's
// explanation.
func newTable(t *testing.T, dsn string, unique bool) string {
	t.Helper()
	name := fmt.Sprintf("public.gw_%d", time.Now().UnixNano())
	exec(t, dsn, `CREATE TABLE `+name+` (
		ingestion_id TEXT NOT NULL, event_id TEXT, occurred_at TEXT, host TEXT)`)
	if unique {
		exec(t, dsn, `CREATE UNIQUE INDEX ON `+name+` (ingestion_id)`)
	}
	t.Cleanup(func() { exec(t, dsn, `DROP TABLE IF EXISTS `+name) })
	return name
}

func exec(t *testing.T, dsn, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func query(t *testing.T, dsn, sql string) []string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s *string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s == nil {
			out = append(out, "NULL")
			continue
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
