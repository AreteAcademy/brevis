package bigquery_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// THE DRY RUN AGAINST REAL BIGQUERY, because the whole cost ceiling rests on
// a claim about somebody else's API: that `dryRun` returns
// `totalBytesProcessed` and bills nothing. A fake agreeing with this file is
// a fake agreeing with this file.
//
// It also pins the shape of the answer: a query over a real table prices
// above zero, and a query over no table at all prices at zero -- which is the
// pair that says the number is about the DATA and not about the request.
func TestADryRunPricesARealQuery(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_price")

	if err := conn.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE %s.t AS SELECT x AS n, REPEAT('a', 100) AS pad FROM UNNEST(GENERATE_ARRAY(1, 5000)) AS x",
		schema)); err != nil {
		t.Fatal(err)
	}

	e, is := conn.(dialect.Estimator)
	if !is {
		t.Fatal("a BigQuery connection cannot price a query")
	}

	over, err := e.Estimate(ctx, "SELECT * FROM "+schema+".t")
	if err != nil {
		t.Fatalf("pricing a table: %v", err)
	}
	if over <= 0 {
		t.Errorf("a table of 5000 rows priced at %d bytes", over)
	}

	// NARROWING THE COLUMNS COSTS LESS, which is the advice the refusal
	// gives. If it did not, the refusal would be telling somebody to do
	// something that does not help.
	narrow, err := e.Estimate(ctx, "SELECT n FROM "+schema+".t")
	if err != nil {
		t.Fatalf("pricing one column: %v", err)
	}
	if narrow >= over {
		t.Errorf("one column priced at %d and every column at %d", narrow, over)
	}

	// AND NOTHING WAS RUN. The table is still the only thing in the schema
	// and the price cost no bytes -- a dry run that created a job would be a
	// dry run somebody is billed for.
	none, err := e.Estimate(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("pricing a constant: %v", err)
	}
	if none != 0 {
		t.Errorf("a query over no table priced at %d bytes", none)
	}
}

// A QUERY OVER ITS CEILING IS REFUSED BY BIGQUERY ITSELF, which is the belt
// the dry run is the brake for. Asserted against the real server because
// `maximumBytesBilled` is a parameter this package sends and the server
// enforces, and the only way to know it is spelt right is to be refused.
func TestTheByteCeilingIsEnforcedByTheServer(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_belt")

	if err := conn.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE %s.t AS SELECT x AS n, REPEAT('a', 100) AS pad FROM UNNEST(GENERATE_ARRAY(1, 5000)) AS x",
		schema)); err != nil {
		t.Fatal(err)
	}
	r := conn.(dialect.Reader)

	// One byte is under anything, so this is refused for the ceiling and not
	// for anything else about the query.
	_, err := r.Read(ctx, dialect.Request{
		Query: "SELECT * FROM " + schema + ".t", Limit: 10, MaxBytes: 1,
	})
	if err == nil {
		t.Fatal("a query with a one-byte ceiling ran")
	}

	// And the same query without one does not.
	if _, err := r.Read(ctx, dialect.Request{
		Query: "SELECT * FROM " + schema + ".t", Limit: 10,
	}); err != nil {
		t.Fatalf("the same query with no ceiling was refused: %v", err)
	}
}

// THE CREDENTIAL PROBE, BOTH DIRECTIONS, AGAINST REAL BIGQUERY. A probe that
// only ever answered one way would be a probe nobody could tell from a
// constant, and this one decides whether `serve` starts.
func TestTheCredentialProbeAnswersBothWays(t *testing.T) {
	ctx, conn, schema := liveSchema(t, "bvs_role")

	p, is := conn.(dialect.WriteProbe)
	if !is {
		t.Fatal("a BigQuery connection cannot be asked whether it may write")
	}

	// THIS credential made the schema a moment ago, so it can write in it.
	can, err := p.CanWrite(ctx, schema)
	if err != nil {
		t.Fatalf("probing a schema this credential owns: %v", err)
	}
	if !can {
		t.Error("the credential that just created a dataset was reported as read-only")
	}

	// A PUBLIC DATASET: everybody reads it and nobody writes it, which is the
	// only read-only corner of BigQuery available to a test without a second
	// service account.
	can, err = p.CanWrite(ctx, "`bigquery-public-data`.samples")
	if can {
		t.Error("this credential was reported able to write to bigquery-public-data")
	}
	if err == nil {
		t.Fatal("a denial arrived with no reason")
	}
	// AND IT WAS DENIED RATHER THAN MISSING. If that dataset is ever gone the
	// probe would answer "no" for the wrong reason and prove nothing, so the
	// permission has to be named in the refusal.
	if !strings.Contains(err.Error(), "bigquery.tables.create") {
		t.Errorf("the refusal is not a permission denial, so this proves nothing: %v", err)
	}
}
