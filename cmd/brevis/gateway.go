package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// cmdGateway is how a gateway appears in the console: its configuration,
// published as a deliberate act, never its traffic and never at runtime.
//
// A command of its own and not a mode of `publish`: workflows and gateways have
// different set semantics, and `publish --prune` must never be able to touch a
// gateway's rows.
func cmdGateway() *cobra.Command {
	c := &cobra.Command{
		Use:   "gateway",
		Short: "Publish what a gateway writes, for the console's /data",
	}
	c.AddCommand(&cobra.Command{
		Use:   "publish <manifest.json>",
		Short: "Store a gateway's manifest (the output of `gateway describe`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			m, err := catalog.ParseManifest(data)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			pool, _, err := open(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := postgres.NewGatewayRepo(pool).Publish(cmd.Context(), m, time.Now()); err != nil {
				return err
			}
			n := 0
			for _, st := range m.Streams {
				n += len(st.Destinations)
			}
			fmt.Printf("  published  %-24s %d streams, %d destinations\n", m.Gateway, len(m.Streams), n)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "unpublish <gateway>",
		Short: "Remove a decommissioned gateway's destinations from /data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pool, _, err := open(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			removed, err := postgres.NewGatewayRepo(pool).Unpublish(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if removed == 0 {
				return fmt.Errorf("no gateway called %q was published", args[0])
			}
			fmt.Printf("  unpublished  %-24s %d destinations\n", args[0], removed)
			return nil
		},
	})
	return c
}
