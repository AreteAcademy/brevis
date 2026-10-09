package bigquery_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
)

// BigQuery CAN be told to throw away what a test leaves behind, and Postgres
// cannot be asked -- so the question is an interface of its own.
var _ dialect.Disposable = bigquery.Dialect{}

// EXECUTED, NOT SPELLED. This package's own comment is the reason: "a dialect
// that returns the right strings and builds the wrong thing passes a test of
// its strings and fails this one." A statement nothing ran is a guess.
//
// It creates a dataset, expires it, and READS THE OPTION BACK OUT OF THE
// CATALOG -- which is the only thing that proves BigQuery took it. Then it
// drops the dataset, and if it is killed before that, the expiry it just set
// is what makes the leak empty itself. The test is its own first customer.
func TestADatasetCanBeToldToThrowItselfAway(t *testing.T) {
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
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	schema := fmt.Sprintf("bvs_disp_%d", time.Now().UnixNano())
	for _, s := range d.EnsureSchema(schema) {
		if err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("making the dataset: %v", err)
		}
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = conn.Exec(bg, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	// A FRESH DATASET HAS NO EXPIRY, which is the whole defect: a harness
	// killed before its cleanup leaves one of these forever.
	if got := expiry(t, ctx, conn, schema); got != "" {
		t.Fatalf("a fresh dataset already expires in %q; this test proves nothing", got)
	}

	if err := conn.Exec(ctx, d.ExpireSchema(schema)); err != nil {
		t.Fatalf("expiring it:\n%s\n\n%v", d.ExpireSchema(schema), err)
	}

	// `1.0` and not `1`: the catalog stores it as a float, and a test
	// asserting `"1"` would pass on a string nobody writes.
	if got := expiry(t, ctx, conn, schema); got != "1.0" {
		t.Errorf("default_table_expiration_days = %q, wanted 1.0", got)
	}
}

// expiry reads the dataset's expiry back out of the catalog, or "" when it
// has none.
//
// `region-us` is where this project's datasets live, and SCHEMATA_OPTIONS is
// region-scoped -- there is no dataset-local spelling of it. Hard-coded
// because this test is about one sandbox in one region, and a test that
// derived the region would be testing that derivation.
func expiry(t *testing.T, ctx context.Context, conn dialect.Conn, schema string) string {
	t.Helper()
	v, err := conn.Scalar(ctx, fmt.Sprintf(
		"SELECT option_value FROM `region-us`.INFORMATION_SCHEMA.SCHEMATA_OPTIONS\n"+
			" WHERE schema_name = '%s' AND option_name = 'default_table_expiration_days'",
		schema))
	if err != nil {
		t.Fatalf("reading the expiry: %v", err)
	}
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
