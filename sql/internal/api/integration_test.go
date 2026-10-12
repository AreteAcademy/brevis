package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
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
