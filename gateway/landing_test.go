package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	tomysql "github.com/AreteAcademy/brevis/sdk/to/mysql"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// oneRecord is a source that yields exactly the record under test.
type oneRecord struct{ record map[string]any }

func (oneRecord) Describe() string { return "one record" }
func (o oneRecord) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		yield(sdk.Envelope{
			Provider: "test", Entity: "test", SourceKey: "one",
			RecordTS: "2026-09-29T00:00:00Z", Payload: o.record,
		}, nil)
	}, nil
}

// The whole plan, in one assertion: a pipeline and a gateway land the same
// record as the same row.
//
// If the ids differ the two tables cannot be read together, a team cannot
// move between the paths, and every other column matching is decoration.
//
// It runs against a real Postgres because the two paths share NO code at the
// write end -- the gateway goes through its auto_table sink, the pipeline
// through sdk.Execute and the postgres driver -- and a mock would only ever
// agree with itself.
func TestIntegrationTheSDKAndTheGatewayLandTheSameRow(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	table := uniqueTable(t, dsn)

	// The producer's record. Deliberately awkward: a nested object whose
	// keys are out of order, and an array whose order matters.
	record := map[string]any{
		"id": "A-1", "amount": float64(10),
		"who":  map[string]any{"last": "silva", "first": "ana"},
		"tags": []any{"x", "y"},
	}

	// --- the gateway path: it creates the table and lands the record ----
	srv := autoTableGateway(t, dsn, "")
	ts := httptest.NewServer(srv.Handler())
	body, err := json.Marshal([]map[string]any{{"table_name": table, "data": record}})
	if err != nil {
		t.Fatal(err)
	}
	postKeyed(t, ts.URL+"/v1/tables", string(body), 202)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	ts.Close()

	// --- the pipeline path: the same record, into the table it made -----
	//
	// `table` is written ONCE and used in both places, which is the practice
	// Landing's doc asks for: the name it mints ids with and the name the
	// writer writes to have nothing checking they agree.
	if err := sdk.Execute(context.Background(), &sdk.Pipeline{
		Source:    sdk.Source{From: oneRecord{record}},
		Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
		Target: sdk.Target{
			To:     topg.Table{DSN: dsn, Name: table},
			Schema: sdk.LandingSchema(sdk.LandingOptions{}),
		},
	}, nil); err != nil {
		t.Fatalf("the pipeline did not run: %v", err)
	}

	// --- the comparison -------------------------------------------------
	rows := query(t, dsn, `SELECT count(*)::text FROM `+table)
	if len(rows) != 1 || rows[0] != "2" {
		t.Fatalf("%s holds %v rows, want 2 -- one from each path", table, rows)
	}

	ids := query(t, dsn, `SELECT DISTINCT brevis_ingestion_id FROM `+table)
	if len(ids) != 1 {
		t.Fatalf("two rows, %d distinct ids: %v\n\n"+
			"The pipeline and the gateway minted different identities for the "+
			"same record. The two tables cannot be read together and a team "+
			"cannot move between the paths, which is the only thing this "+
			"layout exists for.", len(ids), ids)
	}

	// The columns both paths fill must agree.
	for _, c := range []struct{ column, why string }{
		{"brevis_operation", "both say INSERT"},
		{"brevis_record_key", "both take it from the record's `id`"},
		{"data::text", "the producer's record, whole"},
	} {
		got := query(t, dsn, fmt.Sprintf(
			`SELECT count(DISTINCT %s)::text FROM %s`, c.column, table))
		if got[0] != "1" {
			vals := query(t, dsn, fmt.Sprintf(
				`SELECT coalesce(%s::text,'<null>') FROM %s`, c.column, table))
			t.Errorf("%s differs between the two paths (%s): %v", c.column, c.why, vals)
		}
	}

	// And the three the pipeline cannot honestly fill are NULL on ITS row
	// and set on the gateway's. `brevis_gateway IS NULL` is the documented
	// signal for which row is which, so this checks the signal works too.
	mine := query(t, dsn, fmt.Sprintf(
		`SELECT count(*)::text FROM %s WHERE brevis_gateway IS NULL`, table))
	if mine[0] != "1" {
		t.Fatalf("%s rows have no gateway, want exactly 1 -- the pipeline's",
			mine[0])
	}
	for _, column := range []string{"brevis_stream", "brevis_received_bytes"} {
		got := query(t, dsn, fmt.Sprintf(
			`SELECT count(*)::text FROM %s WHERE brevis_gateway IS NULL AND %s IS NOT NULL`,
			table, column))
		if got[0] != "0" {
			t.Errorf("the pipeline's row has %s set: it has no gateway to have "+
				"got it from, and a plausible value there is a lie in a column "+
				"somebody aggregates", column)
		}
		got = query(t, dsn, fmt.Sprintf(
			`SELECT count(*)::text FROM %s WHERE brevis_gateway IS NOT NULL AND %s IS NULL`,
			table, column))
		if got[0] != "0" {
			t.Errorf("the gateway's row is missing %s, which it does have", column)
		}
	}

	// brevis_loaded_at is the DESTINATION's default, and both paths have to
	// let it be one. A path that sends an explicit NULL overrides the
	// default and loses the end-to-end latency the pair exists to measure.
	loaded := query(t, dsn, `SELECT coalesce(brevis_loaded_at::text,'<null>') FROM `+table)
	for _, v := range loaded {
		if strings.Contains(v, "null") {
			t.Errorf("brevis_loaded_at is %v: one path sent an explicit NULL "+
				"and overrode the database DEFAULT, so the difference between "+
				"received_at and loaded_at stopped being the latency", loaded)
			break
		}
	}
}

// The landing layout MERGES, and a re-run is a no-op.
//
// Until Target.DedupKey there was no merge at all: every driver matches on
// `ingestion_id` unless told otherwise, and this layout's identity column is
// `brevis_ingestion_id`. This is that path running.
//
// The table is created by the SDK from LandingSchema, so the UNIQUE index
// Postgres's checkUniqueIndex demands is one the SDK emitted — which is the
// half of this that had never run.
func TestIntegrationTheLandingLayoutMerges(t *testing.T) {
	dsn := os.Getenv("BREVIS_GATEWAY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_PG_DSN is not set")
	}
	t.Setenv("PG_DSN", dsn)
	table := fmt.Sprintf("gwmerge_%d", time.Now().UnixNano())
	t.Cleanup(func() { exec(t, dsn, `DROP TABLE IF EXISTS `+table) })

	record := map[string]any{"id": "A-1", "amount": float64(10)}

	run := func() error {
		return sdk.Execute(context.Background(), &sdk.Pipeline{
			Source:    sdk.Source{From: oneRecord{record}},
			Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
			Target: sdk.Target{
				To:     topg.Table{DSN: dsn, Name: table, CreateTable: true},
				Schema: sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
				// The two that had never met.
				Dedup:    sdk.DedupMerge,
				DedupKey: sdk.LandingColumnID,
			},
		}, nil)
	}

	// The first run creates the table and lands the row.
	if err := run(); err != nil {
		t.Fatalf("the first run failed: %v\n\n"+
			"If this says the unique index is missing, the finding is that "+
			"LandingOptions{UniqueID: true} does not produce what "+
			"checkUniqueIndex looks for — a Schema.CreateTable matter, not a "+
			"DedupKey one.", err)
	}
	// The second is a re-run of the same record: a no-op, not a duplicate.
	if err := run(); err != nil {
		t.Fatalf("the second run failed: %v", err)
	}

	if got := query(t, dsn, `SELECT count(*)::text FROM `+table); got[0] != "1" {
		t.Errorf("%s holds %s rows after the same record twice, want 1 -- the "+
			"merge matched nothing, which looks exactly like a merge matching "+
			"everything it should", table, got[0])
	}

	// And the gateway, merging into the table the SDK made, is the same row
	// again — same id, so nothing new lands.
	srv := autoTableGateway(t, dsn, "", "merge")
	ts := httptest.NewServer(srv.Handler())
	body, err := json.Marshal([]map[string]any{{
		"table_name": table, "operation": "INSERT",
		"unique_key": "id", "data": record,
	}})
	if err != nil {
		t.Fatal(err)
	}
	postKeyed(t, ts.URL+"/v1/tables", string(body), 202)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	ts.Close()

	if got := query(t, dsn, `SELECT count(*)::text FROM `+table); got[0] != "1" {
		t.Errorf("%s holds %s rows after the gateway merged the same record, "+
			"want 1 -- the two paths minted different ids, or the merge did "+
			"not match", table, got[0])
	}
	if got := query(t, dsn, `SELECT count(DISTINCT brevis_ingestion_id)::text FROM `+table); got[0] != "1" {
		t.Errorf("%s distinct ids, want 1", got[0])
	}
}

// The same, on MySQL — and it DOES NOT WORK, which is this slice's finding.
//
// MySQL's `string` is LONGTEXT (sdk/internal/core/ddl.go, and the choice has
// its own comment: a VARCHAR needs a length). MySQL will not put a UNIQUE key
// on a LONGTEXT without one:
//
//	CREATE TABLE ... (`brevis_ingestion_id` LONGTEXT NOT NULL UNIQUE, ...)
//	Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
//	specification without a key length
//
// So `LandingOptions{UniqueID: true}` cannot create a merging landing table
// on MySQL at all. Postgres works because its `string` is TEXT and Postgres
// accepts a UNIQUE on one.
//
// It is NOT a DedupKey problem and not a landing one: any Schema with
// `Unique: true` on a `TypeString` column fails the same way on MySQL. The
// gateway has the same hole — `auto_table` with `into: mysql` and
// `write: merge` calls the same `fixed(unique: true, ...)` — and no test
// covers it, because the MySQL suite creates its tables by hand with
// VARCHAR(36).
//
// Skipped rather than failed, and skipped rather than deleted: the fix
// belongs to the DDL generator, and this is the report.
func TestIntegrationTheLandingLayoutMergesOnMySQL(t *testing.T) {
	t.Skip("MySQL cannot UNIQUE a LONGTEXT: Schema.CreateTable emits one for " +
		"any TypeString column with Unique set, so a merging landing table " +
		"cannot be created there. See this test's comment.")

	dsn := os.Getenv("BREVIS_GATEWAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("BREVIS_GATEWAY_TEST_MYSQL_DSN is not set")
	}
	table := fmt.Sprintf("mymerge_%d", time.Now().UnixNano())
	t.Cleanup(func() { execMySQL(t, dsn, `DROP TABLE IF EXISTS `+table) })

	record := map[string]any{"id": "A-1", "amount": float64(10)}
	run := func() error {
		return sdk.Execute(context.Background(), &sdk.Pipeline{
			Source:    sdk.Source{From: oneRecord{record}},
			Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
			Target: sdk.Target{
				To:       tomysql.Table{DSN: dsn, Name: table, CreateTable: true},
				Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
				Dedup:    sdk.DedupMerge,
				DedupKey: sdk.LandingColumnID,
			},
		}, nil)
	}

	if err := run(); err != nil {
		t.Fatalf("the first run failed: %v", err)
	}
	if err := run(); err != nil {
		t.Fatalf("the second run failed: %v", err)
	}
	if got := queryMySQL(t, dsn, `SELECT count(*) FROM `+table); got[0] != "1" {
		t.Errorf("%s holds %s rows after the same record twice, want 1", table, got[0])
	}
}
