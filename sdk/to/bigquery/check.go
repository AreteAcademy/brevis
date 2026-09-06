package bigquery

import (
	"context"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
	"github.com/AreteAcademy/brevis/sdk/load"
)

// CheckDestination satisfaz core.DestinationChecker.
//
// It exists so the divergence between what the fetcher declares and the real
// table shows up BEFORE the extraction. The check itself is the same one Write
// already did; what changes is the timing, and on a vendor with a quota that is
// the difference between one metadata query and the whole quota window.
func (b Table) CheckDestination(ctx context.Context, columns []string) error {
	if len(columns) == 0 {
		return nil
	}

	cfg, _, err := b.config(core.WriteOptions{Columns: columns})
	if err != nil {
		return err
	}

	loader, err := load.New(ctx, cfg)
	if err != nil {
		return err
	}
	return loader.CheckDestination(ctx, columns)
}
