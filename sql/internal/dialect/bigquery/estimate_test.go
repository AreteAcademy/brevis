package bigquery

import (
	"context"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

func request(query string, limit int, maxBytes int64) dialect.Request {
	return dialect.Request{Query: query, Limit: limit, MaxBytes: maxBytes}
}

// A DRY RUN IS A PRICE, AND IT IS THE ONLY LIMIT THAT WORKS BEFORE THE MONEY
// IS SPENT. BigQuery bills for bytes SCANNED, and a LIMIT does not reduce
// them -- `SELECT * FROM t LIMIT 10` over a petabyte reads the petabyte. So a
// row ceiling bounds the screen and bounds nothing else, and the only thing
// between a typo and an invoice is asking first.
//
// `totalBytesProcessed` comes back from a dry run with nothing billed --
// measured against real BigQuery on 2026-10-09, before this was written.
func TestEstimateAsksForADryRunAndReadsTheBytes(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"totalBytesProcessed":"1099511627776"}`)}
	c := dial(t, f)

	got, err := c.Estimate(context.Background(), "SELECT * FROM d.t")
	if err != nil {
		t.Fatal(err)
	}
	if got != 1099511627776 {
		t.Errorf("estimated %d bytes, and the server said 1099511627776", got)
	}
	if len(f.posts) != 1 {
		t.Fatalf("a price cost %d round trips", len(f.posts))
	}
	if f.posts[0]["dryRun"] != true {
		t.Errorf("the request did not ask for a dry run, so it RAN the query: %v", f.posts[0])
	}
}

// A PRICE NOBODY CAN READ IS NOT A PRICE. A missing or unparseable total must
// not become zero -- zero is under every ceiling, so the refusal that exists
// to stop a large query would wave exactly the queries it cannot measure
// straight through.
func TestATotalThatCannotBeReadIsAnError(t *testing.T) {
	for _, body := range []string{
		`{"jobComplete":true}`,
		`{"jobComplete":true,"totalBytesProcessed":""}`,
		`{"jobComplete":true,"totalBytesProcessed":"lots"}`,
	} {
		c := dial(t, &fake{t: t, reply: ok(body)})
		if _, err := c.Estimate(context.Background(), "SELECT 1"); err == nil {
			t.Errorf("%s was read as a price", body)
		}
	}
}

// THE BELT. The dry run is the brake and this is what stops a query that
// grew between being priced and being run -- a table loaded in between, a
// view over something that changed. BigQuery takes it as a string, because
// its int64 fields travel as strings in JSON, and a number here is a
// parameter the server ignores.
func TestAReadCarriesTheByteCeilingToTheServer(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"schema":{"fields":[{"name":"n"}]},"rows":[{"f":[{"v":"1"}]}],"totalRows":"1"}`)}
	c := dial(t, f)

	if _, err := c.Read(context.Background(), request("SELECT 1", 10, 1<<30)); err != nil {
		t.Fatal(err)
	}
	if got := f.posts[0]["maximumBytesBilled"]; got != "1073741824" {
		t.Errorf("the request carried maximumBytesBilled=%#v, wanted the string \"1073741824\"", got)
	}
}

// NO CEILING MEANS NO FIELD, and not a field saying zero: BigQuery reads
// `maximumBytesBilled: 0` as "bill nothing", which refuses every query.
func TestNoCeilingSendsNoCeiling(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"schema":{"fields":[{"name":"n"}]}}`)}
	c := dial(t, f)

	if _, err := c.Read(context.Background(), request("SELECT 1", 10, 0)); err != nil {
		t.Fatal(err)
	}
	if _, sent := f.posts[0]["maximumBytesBilled"]; sent {
		t.Errorf("a read with no ceiling still sent one: %v", f.posts[0])
	}
}

// A DRY RUN NEVER RETURNS ROWS, so a caller that confused the two would get
// an empty grid and no reason. Reading the statement back proves the price
// and the query are the same text.
func TestThePriceIsForTheQueryThatWasAsked(t *testing.T) {
	const stmt = "SELECT count(*) FROM bronze.orders WHERE day = '2026-10-09'"
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"totalBytesProcessed":"10"}`)}
	c := dial(t, f)

	if _, err := c.Estimate(context.Background(), stmt); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.posts[0]["query"].(string); !strings.EqualFold(got, stmt) {
		t.Errorf("priced %q, and the caller asked for %q", got, stmt)
	}
}
