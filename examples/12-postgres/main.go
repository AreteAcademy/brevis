// A complete Postgres-to-Postgres fetcher.
//
// It exists because phase 5 of the drivers plan asks for one runnable example per
// driver -- and the reason is concrete: it was an example that did not run which
// found the hole in 03-basic-load.
//
//	docker compose -f docker-compose.drivers.yml up -d postgres
//	export PG_DSN='postgres://brevis:brevis@localhost:55432/brevis_it'
//	go run ./12-postgres -create-tables
//	go run ./12-postgres
//	go run ./12-postgres          # the second run loads zero rows
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/AreteAcademy/brevis/sdk"
	frompg "github.com/AreteAcademy/brevis/sdk/from/postgres"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"

	"github.com/jackc/pgx/v5"
)

const (
	src = "example_orders"
	dst = "landing_orders"
)

func main() {
	var create bool

	sdk.Run(sdk.Pipeline{
		Name:  "exemplo_postgres",
		Flags: func(fs *flag.FlagSet) { fs.BoolVar(&create, "create-tables", false, "creates the tables and exits") },

		Before: func(ctx context.Context, _ *sdk.Pipeline) error {
			if !create {
				return nil
			}
			if err := createTables(ctx); err != nil {
				return err
			}
			fmt.Println("tables created; run again without -create-tables")
			os.Exit(0)
			return nil
		},

		Source: sdk.Source{
			From: frompg.Query{
				DSN: os.Getenv("PG_DSN"),
				// Pagination by KEY. OFFSET on a large table is O(n^2), because
				// the server counts the rows it discards.
				SQL: "SELECT id, name, amount, updated_at FROM " + src +
					" WHERE id > $1 ORDER BY id LIMIT $2",
				Args: []any{0, 10_000},
			},
			Preview: 3,
		},

		Transform: []sdk.Transformer{
			// The ingestion_id's key is text: a number and its string have
			// de produzir o mesmo id.
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return fmt.Sprint(r["id"]), nil
			}),
			sdk.Without("id"),
			sdk.Rename(map[string]string{"updated_at": "record_ts"}),
			sdk.Compute("provider", func(map[string]any) (any, error) { return "example", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "orders", nil }),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		},

		Target: sdk.Target{
			To: topg.Table{DSN: os.Getenv("PG_DSN"), Name: dst},
			Columns: []string{
				"ingestion_id", "ingestion_loaded_at", "provider", "entity",
				"source_key", "record_ts", "name", "amount",
			},
			// Requires a unique index on ingestion_id; -create-tables creates it.
			Dedup: sdk.DedupMerge,
		},
	})
}

// createTables writes the DDL by hand, on purpose: the driver creates no table
// and infers no type, so the DDL is the consumer's -- and they are the one who
// knows `amount` is NUMERIC(18,2) and not a float.
func createTables(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, os.Getenv("PG_DSN"))
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS ` + src + ` (
			id INT PRIMARY KEY, name TEXT, amount NUMERIC(18,2), updated_at TIMESTAMPTZ)`,
		`INSERT INTO ` + src + `
			SELECT g, 'order ' || g, (g * 1.5)::numeric, now() FROM generate_series(1, 500) g
			ON CONFLICT (id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS ` + dst + ` (
			ingestion_id TEXT NOT NULL,
			ingestion_loaded_at TIMESTAMPTZ NOT NULL,
			provider TEXT NOT NULL,
			entity TEXT NOT NULL,
			source_key TEXT NOT NULL,
			record_ts TEXT NOT NULL,
			name TEXT,
			amount NUMERIC(18,2))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + dst + `_ingestion_id
			ON ` + dst + ` (ingestion_id)`,
	}
	for _, sql := range ddl {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("ddl: %w", err)
		}
	}
	return nil
}
