package gateway_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	_ "github.com/go-sql-driver/mysql"
)

// The same two words against MySQL, asserted by reading the table back.
//
// `merge` has to mean in MySQL what it means in Postgres, and the machinery
// underneath is different -- INSERT IGNORE rather than ON CONFLICT DO NOTHING.
// One word with two meanings in the same config file would be the worst
// outcome, and only a test against both databases says it did not happen.
func TestIntegrationMySQLWriteModes(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_MYSQL_DSN is not set")
	}

	for _, c := range []struct {
		mode   string
		unique bool
		want   string
	}{
		{mode: "append", unique: false, want: "first,second"},
		{mode: "merge", unique: true, want: "first"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			table := newMySQLTable(t, dsn, c.unique)
			t.Setenv("MY_DSN", dsn)

			srv := mysqlGateway(t, table, c.mode)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			post(t, ts.URL+"/v1/clicks", event("e-1", "first"), http.StatusAccepted)
			post(t, ts.URL+"/v1/clicks", event("e-1", "second"), http.StatusAccepted)
			if err := srv.Close(context.Background()); err != nil {
				t.Fatalf("draining: %v", err)
			}

			rows := queryMySQL(t, dsn, `SELECT host FROM `+table+` ORDER BY host`)
			if got := strings.Join(rows, ","); got != c.want {
				t.Errorf("%s left %q, want %q", c.mode, got, c.want)
			}
		})
	}
}

// merge onto a MySQL table with no unique index is refused, for the reason it
// is in Postgres: INSERT IGNORE has nothing to match, so every redelivery would
// land again in a table whose owner asked for the opposite.
func TestIntegrationMySQLMergeRefusesATableWithoutTheIndex(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_MYSQL_DSN is not set")
	}
	table := newMySQLTable(t, dsn, false)
	t.Setenv("MY_DSN", dsn)

	dead := t.TempDir()
	srv := mysqlGateway(t, table, "merge", dead)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post(t, ts.URL+"/v1/clicks", event("e-1", "first"), http.StatusAccepted)
	_ = srv.Close(context.Background())

	if rows := queryMySQL(t, dsn, `SELECT host FROM `+table); len(rows) != 0 {
		t.Errorf("it wrote anyway: %v", rows)
	}
	if reason := buriedReason(t, dead); !strings.Contains(reason, "unique index") {
		t.Errorf("the dead letter does not carry the reason: %s", reason)
	}
}

func mysqlGateway(t *testing.T, table, mode string, dead ...string) *gateway.Server {
	t.Helper()
	path := t.TempDir()
	if len(dead) > 0 {
		path = dead[0]
	}
	yaml := fmt.Sprintf(`
name: my_gateway
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    buffer: {flush: {records: 500, every: 1h}}
    retry: {attempts: 1}
    sink: {type: mysql, dsn_from: MY_DSN, table: %s, write: %s}
    dead_letter: {type: files, path: %s/}
`, table, mode, path)

	file := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(file)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	srv, err := gateway.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func newMySQLTable(t *testing.T, dsn string, unique bool) string {
	t.Helper()
	name := fmt.Sprintf("gw_%d", time.Now().UnixNano())
	execMySQL(t, dsn, `CREATE TABLE `+name+` (
		ingestion_id VARCHAR(36) NOT NULL, event_id VARCHAR(64),
		occurred_at VARCHAR(64), host VARCHAR(255))`)
	if unique {
		execMySQL(t, dsn, `CREATE UNIQUE INDEX idx_`+name+` ON `+name+` (ingestion_id)`)
	}
	t.Cleanup(func() { execMySQL(t, dsn, `DROP TABLE IF EXISTS `+name) })
	return name
}

func openMySQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func execMySQL(t *testing.T, dsn, q string) {
	t.Helper()
	db := openMySQL(t, dsn)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func queryMySQL(t *testing.T, dsn, q string) []string {
	t.Helper()
	db := openMySQL(t, dsn)
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if !s.Valid {
			out = append(out, "NULL")
			continue
		}
		out = append(out, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
