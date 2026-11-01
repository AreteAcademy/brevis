package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RunPrune applies a retention policy once at start and then on every tick,
// until ctx ends. A failed prune is logged and retried on the next tick: the
// policy is idempotent and a retention hiccup must never stop the scheduler.
//
// The tick is a parameter so a test drives it without waiting.
func RunPrune(ctx context.Context, tick <-chan time.Time,
	prune func(context.Context) (fmt.Stringer, error), log *slog.Logger) {
	for {
		if report, err := prune(ctx); err != nil {
			log.Error("scheduled prune failed", "error", err)
		} else {
			log.Info("scheduled prune", "report", report.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
	}
}
