package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// canceler is the one call `brevis cancel` makes, so the answer can be tested
// without a database.
type canceler interface {
	RequestCancel(ctx context.Context, id uuid.UUID, actor string) (postgres.CancelResult, error)
}

// cancelRun asks to cancel the run as `cli` -- the actor the events feed shows
// -- and says which of the three things happened. "cancel requested" is not
// "canceled": a running run stops when its dispatcher next reads the requests.
func cancelRun(ctx context.Context, c canceler, arg string) (string, error) {
	id, err := uuid.Parse(arg)
	if err != nil {
		return "", fmt.Errorf("%q is not a run id: %w", arg, err)
	}
	got, err := c.RequestCancel(ctx, id, "cli")
	if err != nil {
		return "", err
	}
	switch got.Outcome {
	case postgres.CancelDone:
		return fmt.Sprintf("canceled %s", id), nil
	case postgres.CancelRequested:
		return fmt.Sprintf("cancel requested for %s; its dispatcher stops it", id), nil
	default:
		return fmt.Sprintf("already ended: %s (%s)", got.Status, id), nil
	}
}

func cmdCancel() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a run that is waiting or running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pool, _, err := open(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			said, err := cancelRun(ctx, postgres.NewRunRepo(pool), args[0])
			if err != nil {
				return err
			}
			fmt.Println("  " + said)
			return nil
		},
	}
}
