package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// livePostgres is a throwaway schema on a real Postgres, gated the way the
// conformance suite is: by BREVIS_SQL_IT_DSN, which CI sets and asserts did
// not skip.
func livePostgres(t *testing.T) (context.Context, dialect.Conn, string, string) {
	t.Helper()
	dsn := os.Getenv("BREVIS_SQL_IT_DSN")
	if dsn == "" {
		t.Skip("BREVIS_SQL_IT_DSN not set")
	}
	ctx := context.Background()
	d := postgres.Dialect{}
	conn, err := d.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("bvs_api_%d", time.Now().UnixNano())
	if err := dialecttest.MakeThrowawaySchema(ctx, d, conn, schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = conn.Close(context.Background())
	})
	return ctx, conn, schema, dsn
}

func liveServer(t *testing.T, dsn string, endpoints ...*Endpoint) http.Handler {
	t.Helper()
	for _, e := range endpoints {
		if err := e.parse(postgres.Dialect{}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := New(&Config{Name: "it", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 100,
		Open:      func(ctx context.Context) (dialect.Conn, error) { return postgres.Dialect{}.Open(ctx, dsn) },
		Endpoints: endpoints})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A WRITE HIDDEN IN A TEMPLATE IS REFUSED BY THE WAREHOUSE. Not by a parser
// reading the SQL -- by Postgres, inside the READ ONLY transaction the read
// runs in -- and the row is still what it was.
func TestIntegrationAWriteInATemplateIsRefusedByPostgres(t *testing.T) {
	ctx, conn, schema, dsn := livePostgres(t)
	if err := conn.Exec(ctx, "CREATE TABLE "+schema+".t AS SELECT 'local'::text AS w, 1 AS v"); err != nil {
		t.Fatal(err)
	}
	h := liveServer(t, dsn,
		&Endpoint{Name: "writes", Path: "/v1/writes",
			SQL: "UPDATE " + schema + ".t SET v = 2 WHERE w = {{ tenant }} RETURNING v"},
		&Endpoint{Name: "reads", Path: "/v1/reads",
			SQL: "SELECT v FROM " + schema + ".t WHERE w = {{ tenant }}"})

	if rec := get(h, "/v1/writes"); rec.Code != http.StatusBadGateway {
		t.Errorf("the UPDATE answered %d %s", rec.Code, rec.Body)
	}
	rec := get(h, "/v1/reads")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `{"v":1}`) {
		t.Errorf("after the refused write the row reads %d %s", rec.Code, rec.Body)
	}
}

// THE TENANT MATRIX, against a real Postgres. Two tenants in one table;
// acme's key sends globex's id in every parameter, in every encoding tried,
// and every answer is either acme's own rows or a 400. Never a globex row.
func TestIntegrationNoParameterReachesAnotherTenantsRows(t *testing.T) {
	ctx, conn, schema, dsn := livePostgres(t)
	if err := conn.Exec(ctx, "CREATE TABLE "+schema+".t AS "+
		"SELECT 'acme'::text AS tenant_id, 'c1'::text AS c, 1 AS v UNION ALL "+
		"SELECT 'globex', 'c1', 2 UNION ALL SELECT 'globex', 'globex', 3"); err != nil {
		t.Fatal(err)
	}
	tenantMatrix(t, postgres.Dialect{}, schema+".t",
		func(ctx context.Context) (dialect.Conn, error) { return postgres.Dialect{}.Open(ctx, dsn) })
}

// THE SAME MATRIX ON BIGQUERY, where binding is by name and typed by this
// code rather than inferred by the server -- a different way to be wrong.
func TestIntegrationBigQueryNoParameterReachesAnotherTenantsRows(t *testing.T) {
	project := os.Getenv("BREVIS_SQL_IT_BQ_PROJECT")
	if project == "" {
		t.Skip("BREVIS_SQL_IT_BQ_PROJECT not set")
	}
	ctx := context.Background()
	d := bigquery.Dialect{}
	conn, err := d.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("bvs_api_%d", time.Now().UnixNano())
	if err := dialecttest.MakeThrowawaySchema(ctx, d, conn, schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = conn.Close(context.Background())
	})
	if err := conn.Exec(ctx, "CREATE TABLE "+schema+".t AS "+
		"SELECT 'acme' AS tenant_id, 'c1' AS c, 1 AS v UNION ALL "+
		"SELECT 'globex', 'c1', 2 UNION ALL SELECT 'globex', 'globex', 3"); err != nil {
		t.Fatal(err)
	}
	tenantMatrix(t, d, schema+".t",
		func(ctx context.Context) (dialect.Conn, error) { return d.Open(ctx, project) })
}

func tenantMatrix(t *testing.T, d dialect.Dialect, table string, open func(context.Context) (dialect.Conn, error)) {
	t.Helper()
	acme, globex := "acme-key-0123456789abcdef0123456789", "globex-key-0123456789abcdef012345678"
	t.Setenv("IT_API_KEYS", hashOf(acme)+"=acme\n"+hashOf(globex)+"=globex")

	e := &Endpoint{Name: "t", Path: "/v1/t",
		Params: map[string]Param{
			"c":     {Type: "string", MaxLen: 64},
			"n":     {Type: "int"},
			"order": {Type: "enum", Values: []string{"asc", "desc"}, Default: ptr("asc")},
		},
		SQL: "SELECT tenant_id, v FROM " + table + " WHERE tenant_id = {{ tenant }}" +
			"{% if c %} AND c = {{ c }}{% endif %}{% if n %} AND v = {{ n }}{% endif %} ORDER BY v {{ order }}"}
	if err := e.check(); err != nil {
		t.Fatal(err)
	}
	if err := e.parse(d); err != nil {
		t.Fatal(err)
	}
	h, err := New(&Config{Name: "it", Env: "production", MaxRows: 100, Dialect: d,
		Listen:    Listen{Auth: &Auth{Type: "bearer", KeysFrom: "IT_API_KEYS"}},
		Open:      open,
		Limits:    Limits{MaxBytesBilled: 1 << 30},
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}

	attacks := []string{
		"", "c=globex", "c=c1", "c=%27%20OR%20tenant_id%3D%27globex", "c=globex%27%20--",
		"c=%27%20OR%201%3D1%20--", "c=c1%27%20UNION%20SELECT%20tenant_id%2C%20v%20FROM%20" + table + "%20--",
		"c=%24%31", "c=%40tenant", "c=%7B%7B%20tenant%20%7D%7D", "c=gl%C3%B6bex", "c=c1%00",
		"tenant=globex", "c=c1&tenant=globex", "Tenant=globex", "c=c1;tenant=globex",
		"n=1%20OR%201%3D1", "n=2", "n=-0", "order=desc", "order=asc%2C%20(SELECT%201)",
		"order=desc%3B%20DROP%20TABLE%20" + table, "c=c1&c=globex",
	}
	readOwn := 0
	for _, q := range attacks {
		rec := withKey(h, "/v1/t?"+q, acme)
		switch rec.Code {
		case http.StatusOK:
			var body struct {
				Data []struct {
					Tenant string `json:"tenant_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			for _, row := range body.Data {
				if row.Tenant != "acme" {
					t.Errorf("acme's key read a %s row with %q", row.Tenant, q)
				}
			}
			if len(body.Data) > 0 {
				readOwn++
			}
		case http.StatusBadRequest:
		default:
			t.Errorf("%q answered %d %s", q, rec.Code, rec.Body)
		}
	}
	// NOT VACUOUS: a matrix where every request failed would pass the loop.
	if readOwn == 0 {
		t.Error("no request in the matrix read acme's own rows")
	}

	// and the table is still there, with its three rows
	rec := withKey(h, "/v1/t", globex)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"v":3`) {
		t.Errorf("globex's own read after the matrix: %d %s", rec.Code, rec.Body)
	}
}
