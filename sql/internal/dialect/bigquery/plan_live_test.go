package bigquery_test

import (
	"fmt"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// WHAT BIGQUERY CALLS A STATEMENT, ASKED OF BIGQUERY.
//
// The cost ceiling already rests on a claim about somebody else's API, and
// `estimate_live_test.go` exists because a fake agreeing with a fake proves
// nothing. This rests on a second claim out of the same response: that a dry
// run reports `statementType`, and reports it for the shapes that write.
//
// IT IS WHAT LETS A WRITABLE CREDENTIAL BE ACCEPTED HERE. Postgres can be told
// to refuse writes and then refuses them itself; BigQuery has no read-only
// session and has this instead. If the field ever stops arriving, a writable
// credential must go back to being refused -- so this test is the thing that
// would say so.
func TestADryRunSaysWhatTheStatementIs(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_kind")

	planner, can := conn.(dialect.Planner)
	if !can {
		t.Fatal("a BigQuery connection cannot say what a statement is")
	}
	if err := conn.Exec(ctx, "CREATE TABLE "+schema+".t AS SELECT 1 AS n"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		why       string
		statement string
		kind      string
		reads     bool
	}{
		{"a plain query", "SELECT * FROM " + schema + ".t", "SELECT", true},
		{"a CTE is still a query", "WITH a AS (SELECT * FROM " + schema + ".t) SELECT * FROM a", "SELECT", true},
		{"DDL", "CREATE TABLE " + schema + ".u (n INT64)", "CREATE_TABLE", false},
		{"a view is DDL too", "CREATE OR REPLACE VIEW " + schema + ".v AS SELECT 1 AS n", "CREATE_VIEW", false},
		{"an insert", "INSERT INTO " + schema + ".t (n) VALUES (2)", "INSERT", false},
		{"an update", "UPDATE " + schema + ".t SET n = 9 WHERE TRUE", "UPDATE", false},
		{"a delete", "DELETE FROM " + schema + ".t WHERE TRUE", "DELETE", false},
		{"a merge", "MERGE " + schema + ".t t USING (SELECT 1 n) s ON t.n = s.n " +
			"WHEN NOT MATCHED THEN INSERT (n) VALUES (s.n)", "MERGE", false},
		{"a truncate", "TRUNCATE TABLE " + schema + ".t", "TRUNCATE_TABLE", false},
		// THE TWO THAT MATTER MOST, because neither looks like a write to
		// anything reading the first keyword.
		{"anything multi-statement", "SELECT 1; DROP TABLE " + schema + ".t;", "SCRIPT", false},
		{"a write to a BUCKET, which ends in a SELECT",
			"EXPORT DATA OPTIONS(uri='gs://zarv-development-94b6-bravis-it/probe/*.csv', " +
				"format='CSV', overwrite=true) AS SELECT * FROM " + schema + ".t", "EXPORT_DATA", false},
	} {
		t.Run(c.why, func(t *testing.T) {
			got, err := planner.Plan(ctx, c.statement)
			if err != nil {
				t.Fatalf("planning: %v", err)
			}
			if got.Kind != c.kind {
				t.Errorf("bigquery calls it %q, this test was written against %q", got.Kind, c.kind)
			}
			if got.Reads != c.reads {
				t.Errorf("Reads is %v for a %s", got.Reads, got.Kind)
			}
		})
	}

	// AND THE PRICE STILL COMES OUT OF THE SAME CALL. The verdict was added
	// to the dry run that was already being made; a Plan that stopped
	// carrying bytes would make every query cost a second round trip to
	// learn the other half.
	t.Run("one call carries the price too", func(t *testing.T) {
		got, err := planner.Plan(ctx, "SELECT * FROM "+schema+".t")
		if err != nil {
			t.Fatal(err)
		}
		if got.Bytes <= 0 {
			t.Errorf("a query over a real table priced at %d bytes", got.Bytes)
		}
	})

	// A statement the warehouse cannot parse is an ERROR and never a verdict.
	t.Run("nonsense is an error and not a read", func(t *testing.T) {
		if _, err := planner.Plan(ctx, fmt.Sprintf("SELECT * FROM %s.nope_%d", schema, 1)); err == nil {
			t.Error("a missing table planned without complaint")
		}
	})
}

// WHAT A WRITE ANSWERS, ASKED OF BIGQUERY.
//
// It says it by OMISSION: `numDmlAffectedRows` arrives on a DML and is absent
// on a DDL. Absent, not zero -- which is the same distinction Postgres makes
// through its command tag, reached from the other side. A field read as zero
// would print "0 rows" for a table that had just been created.
func TestWhatBigQuerySaysAWriteChanged(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_write")

	w, can := conn.(dialect.Writer)
	if !can {
		t.Fatal("this dialect cannot run a statement that changes data")
	}

	for _, c := range []struct {
		why       string
		statement string
		counted   bool
		changed   int64
	}{
		{"DDL counts no rows", "CREATE TABLE " + schema + ".w (n INT64)", false, 0},
		{"an insert counts what it inserted",
			"INSERT INTO " + schema + ".w (n) VALUES (1),(2),(3)", true, 3},
		{"an update counts what it matched",
			"UPDATE " + schema + ".w SET n = 9 WHERE n > 1", true, 2},
		{"an update that matched nothing counts ZERO",
			"UPDATE " + schema + ".w SET n = 0 WHERE n = 4242", true, 0},
		{"a delete counts what it removed", "DELETE FROM " + schema + ".w WHERE n = 9", true, 2},
	} {
		t.Run(c.why, func(t *testing.T) {
			got, err := w.Write(ctx, c.statement)
			if err != nil {
				t.Fatalf("running it: %v", err)
			}
			if got.Counted != c.counted {
				t.Errorf("Counted is %v, want %v", got.Counted, c.counted)
			}
			if got.Counted && got.Changed != c.changed {
				t.Errorf("it changed %d row(s), want %d", got.Changed, c.changed)
			}
			if len(got.Columns) != 0 || len(got.Rows) != 0 {
				t.Errorf("a write came back with %d column(s) and %d row(s)",
					len(got.Columns), len(got.Rows))
			}
		})
	}
}
