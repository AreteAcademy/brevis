package bigquery_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
)

// Reading a result SET is a capability of its own, asserted and not assumed.
//
// `Conn` has Exec and Scalar -- "the two the build loop needs" -- and a grid
// needs neither. `Reader` is the Disposable pattern again: off the production
// interface, type-asserted by the one caller that wants it, so the build loop
// cannot reach it and a dialect that cannot read is refused rather than
// discovered at runtime.
func TestABigQueryConnectionCanReadAResultSet(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_read")

	if err := conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s.t AS
		SELECT 1 AS k, 'a' AS v, TIMESTAMP '2026-01-01' AS w, CAST(NULL AS STRING) AS n
		UNION ALL SELECT 2, 'b', TIMESTAMP '2026-01-02', 'here'`, schema)); err != nil {
		t.Fatal(err)
	}

	r, is := conn.(dialect.Reader)
	if !is {
		t.Fatal("a BigQuery connection cannot read a result set")
	}
	got, err := r.Read(ctx, dialect.Request{Query: "SELECT k, v, w, n FROM " + schema + ".t ORDER BY k", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	// THE COLUMN NAMES ARE THE POINT. A grid with the right rows under the
	// wrong headings is worse than no grid, and nothing in the conn read the
	// response's schema before this.
	want := []string{"k", "v", "w", "n"}
	if len(got.Columns) != len(want) {
		t.Fatalf("columns = %v, wanted %v", got.Columns, want)
	}
	for i := range want {
		if got.Columns[i] != want[i] {
			t.Errorf("column %d is %q, wanted %q", i, got.Columns[i], want[i])
		}
	}

	if len(got.Rows) != 2 {
		t.Fatalf("%d rows, wanted 2", len(got.Rows))
	}
	// NULL IS NOT THE EMPTY STRING, and the grid has to be able to tell them
	// apart: one is "nothing was recorded" and the other is a value somebody
	// wrote. They render differently, so they must arrive differently.
	if got.Rows[0][3] != nil {
		t.Errorf("a NULL came back as %#v", got.Rows[0][3])
	}
	if fmt.Sprint(got.Rows[1][3]) != "here" {
		t.Errorf("row 2 column n is %#v", got.Rows[1][3])
	}
	if got.Truncated {
		t.Error("two rows under a limit of ten reported truncation")
	}
}

// THE LIMIT IS THE SERVER'S, and it CUTS rather than refuses: a preview that
// errored because a table is large would be a preview that never works on the
// tables somebody actually has.
//
// And it says that it cut. A grid showing ten rows of a million, silently, is
// a grid that lies -- somebody reads a MAX off it and is wrong.
func TestTheLimitCutsAndSaysSo(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_lim")

	if err := conn.Exec(ctx,
		"CREATE TABLE "+schema+".many AS SELECT n FROM UNNEST(GENERATE_ARRAY(1, 50)) AS n"); err != nil {
		t.Fatal(err)
	}

	r := conn.(dialect.Reader)
	got, err := r.Read(ctx, dialect.Request{Query: "SELECT n FROM " + schema + ".many", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 5 {
		t.Fatalf("%d rows came back under a limit of 5", len(got.Rows))
	}
	if !got.Truncated {
		t.Error("five rows of fifty, and it did not say it had cut")
	}
}

// liveSchema opens a connection and makes a throwaway dataset for one test.
func liveSchema(t *testing.T, prefix string) (context.Context, dialect.Conn, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipped under -short")
	}
	project := os.Getenv("BREVIS_SQL_IT_BQ_PROJECT")
	if project == "" {
		t.Skip("BREVIS_SQL_IT_BQ_PROJECT not set")
	}
	ctx := context.Background()
	d := bigquery.Dialect{}
	conn, err := d.Open(ctx, project)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if err := dialecttest.MakeThrowawaySchema(ctx, d, conn, schema); err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = conn.Exec(bg, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close(bg)
	})
	return ctx, conn, schema
}
