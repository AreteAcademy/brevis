// Step three: the Go SDK takes the aggregate back and lands it.
//
// Polars produced 120-odd rows and stopped. Everything after that -- provenance,
// the deduplication key, a transaction, a retry -- is the engine's job, and it
// is the same code it would be if the middle step had never existed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	bctx "github.com/AreteAcademy/brevis/sdk/context"
	"github.com/AreteAcademy/brevis/sdk/from"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"

	"github.com/jackc/pgx/v5"
)

const table = "shop_client_revenue"

func main() {
	var create bool
	flag.BoolVar(&create, "create-table", false, "creates the table and exits")
	flag.Parse()

	ctx := context.Background()
	if create {
		if err := createTable(ctx); err != nil {
			log.Fatal(err)
		}
		fmt.Println("table created; run again without -create-table")
		return
	}

	// What Polars wrote. Reading the path from the context is what makes the
	// two languages one pipeline instead of two scripts that agree by luck.
	shaped, err := bctx.String("shape.path")
	if err != nil {
		log.Fatalf("reading the path from shape: %v", err)
	}
	if shaped == "" {
		log.Fatal("shape published no path: run the whole workflow, not this step alone")
	}

	data, err := sdk.Extract(ctx, sdk.Source{
		From:    from.Files{Path: shaped},
		Preview: 3,
	})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	// Resolved ONCE, here. A Compute runs per record, so calling runDate()
	// inside it logged the same line 120 times on the first run of this -- and
	// would have read the clock 120 times in a job that straddles midnight.
	day := runDate()

	data = sdk.Transform(data,
		// client_id is the grain: one row per client, per load. Keying on it
		// makes a re-run replace rather than duplicate -- which matters here
		// more than usual, because re-running is how anybody iterating on
		// shape.py works.
		sdk.Compute("source_key", func(r map[string]any) (any, error) {
			return fmt.Sprint(r["client_id"]), nil
		}),
		sdk.Compute("provider", func(map[string]any) (any, error) { return "shop", nil }),
		sdk.Compute("entity", func(map[string]any) (any, error) { return "client_revenue", nil }),

		// An aggregate has no timestamp of its own -- it is a snapshot, and the
		// only honest date is the one the RUN is for. BREVIS_AUTO_DATE is that
		// date, so a retry of Tuesday's slot on Thursday still writes Tuesday's
		// ids and replaces Tuesday's rows instead of inventing a third day.
		//
		// Taking time.Now() here would make every retry a new row, which is the
		// failure that looks like success: the table grows and every number in
		// it is right.
		sdk.Compute("record_ts", func(map[string]any) (any, error) { return day, nil }),
		sdk.IngestionID(),
		sdk.IngestionLoadedAt(),
	)

	res, err := sdk.Load(ctx, data, sdk.Target{
		To: topg.Table{DSN: os.Getenv("PG_DSN"), Name: table},
		Columns: []string{
			"ingestion_id", "ingestion_loaded_at", "provider", "entity",
			"source_key", "record_ts", "client_id", "orders", "units", "revenue", "avg_ticket",
		},
		Dedup: sdk.DedupMerge,
	})
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	fmt.Println(res)
}

// runDate is the date this run is FOR, which the engine supplies. Running the
// step by hand outside a run falls back to today, and says so in the log rather
// than silently -- a value this far upstream of the ingestion_id should never
// change without somebody reading a line about it.
//
// Called once per run, never per record: see the call site.
func runDate() string {
	if d := os.Getenv("BREVIS_AUTO_DATE"); d != "" {
		return d
	}
	today := time.Now().UTC().Format("2006-01-02")
	log.Printf("BREVIS_AUTO_DATE is unset (not running under the engine); using %s", today)
	return today
}

// createTable writes the DDL by hand, as 12-postgres does and for the same
// reason: the driver creates nothing and infers nothing, so the types are the
// consumer's call. revenue is NUMERIC, not double precision -- money that goes
// through a float comes back wrong by a cent, eventually.
func createTable(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, os.Getenv("PG_DSN"))
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS ` + table + ` (
			ingestion_id TEXT NOT NULL,
			ingestion_loaded_at TIMESTAMPTZ NOT NULL,
			provider TEXT NOT NULL,
			entity TEXT NOT NULL,
			source_key TEXT NOT NULL,
			record_ts TEXT NOT NULL,
			client_id TEXT NOT NULL,
			orders INT NOT NULL,
			units INT NOT NULL,
			revenue NUMERIC(18,2) NOT NULL,
			avg_ticket NUMERIC(18,2) NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + table + `_ingestion_id
			ON ` + table + ` (ingestion_id)`,
	}
	for _, sql := range ddl {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("ddl: %w", err)
		}
	}
	return nil
}
