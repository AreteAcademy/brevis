package api

import (
	"context"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// CachedFailing keeps r's answer for ttl. The badge is drawn on every page,
// and the list costs ~6 ms with a thousand workflows and fifty thousand runs
// (measured for run-diagnosis); a count that is fifteen seconds old says the
// same thing to whoever reads it.
//
// An error is not kept: the next page asks again.
func CachedFailing(r FailingReader, ttl time.Duration, now func() time.Time) FailingReader {
	return &cachedFailing{r: r, list: cached(r.FailingWorkflows, ttl, now)}
}

type cachedFailing struct {
	r    FailingReader
	list func(context.Context) ([]postgres.FailingWorkflow, error)
}

func (c *cachedFailing) FailingWorkflows(ctx context.Context) ([]postgres.FailingWorkflow, error) {
	return c.list(ctx)
}

// FailureStreak is not cached: it is one workflow's, read for the run open on
// the screen, and a stale count beside a fresh failure would contradict it.
func (c *cachedFailing) FailureStreak(ctx context.Context, slug string) (int, error) {
	return c.r.FailureStreak(ctx, slug)
}

// Fresh reads past the cache, for the Overview's list: a workflow that just
// passed must leave it at the next look.
func (c *cachedFailing) Fresh(ctx context.Context) ([]postgres.FailingWorkflow, error) {
	return c.r.FailingWorkflows(ctx)
}
