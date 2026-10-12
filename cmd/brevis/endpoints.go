package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// cmdEndpoints is how a brevis-sql-api appears in the console: what it
// serves, published as a deliberate act, never its traffic and never at
// runtime -- the gateway's shape, for the gateway's reasons.
//
// A command of its own and not a mode of `gateway`: an API reads and a
// gateway writes, and `gateway unpublish` must never be able to remove one.
func cmdEndpoints() *cobra.Command {
	c := &cobra.Command{
		Use:   "endpoints",
		Short: "Publish what a brevis-sql-api serves, for the console's Data → Endpoints",
	}
	c.AddCommand(&cobra.Command{
		Use:   "publish <manifest.json>",
		Short: "Store an API's manifest (the output of `brevis-sql-api describe`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			m, err := catalog.ParseEndpoints(data)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			pool, _, err := open(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := postgres.NewEndpointRepo(pool).Publish(cmd.Context(), m, time.Now()); err != nil {
				return err
			}
			fmt.Printf("  published  %-24s %d endpoints\n", m.Name, len(m.Endpoints))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "unpublish <api>",
		Short: "Remove a decommissioned API's endpoints from the console",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pool, _, err := open(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			removed, err := postgres.NewEndpointRepo(pool).Unpublish(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if removed == 0 {
				return fmt.Errorf("no API called %q was published", args[0])
			}
			fmt.Printf("  unpublished  %-24s %d endpoints\n", args[0], removed)
			return nil
		},
	})
	return c
}
