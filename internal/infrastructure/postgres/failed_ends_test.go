package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/events"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// beforeTheEnds is the last migration before the one that fills in when the
// runs that had already failed ended.
const beforeTheEnds = 28

func endedAt(t *testing.T, pool *postgres.Pool, id uuid.UUID) *time.Time {
	t.Helper()
	var ended *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT terminado_em FROM runs WHERE id = $1`, id).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	return ended
}

// A RUN THAT FAILS HAS ENDED, like one that succeeded or was canceled: the
// feed and the insights read when a run ended, and a failure without it was
// left out of both.
func TestFailingRecordsWhenTheRunEnded(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	id := newRun(t, repo)
	for _, to := range []dom.Status{dom.StatusQueued, dom.StatusRunning, dom.StatusFailed} {
		if err := repo.Transicionar(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}
	if endedAt(t, pool, id) == nil {
		t.Fatal("terminado_em is NULL after failed")
	}

	// A retry is the run going again: back in the queue it has not ended.
	for _, to := range []dom.Status{dom.StatusRetrying, dom.StatusQueued} {
		if err := repo.Transicionar(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}
	if ended := endedAt(t, pool, id); ended != nil {
		t.Errorf("terminado_em = %v back in the queue, wanted NULL", ended)
	}
}

// The feed shows it.
func TestAFailedRunIsInTheFeed(t *testing.T) {
	pool := insightsDB(t)
	repo := postgres.NewRunRepo(pool)
	ctx := context.Background()
	id := newRun(t, repo)
	for _, to := range []dom.Status{dom.StatusQueued, dom.StatusRunning, dom.StatusFailed} {
		if err := repo.Transicionar(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}

	got, _, err := postgres.NewEventRepo(pool).RecentEvents(ctx, postgres.EventQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Kind == events.RunFailed && e.RunID != nil && *e.RunID == id {
			return
		}
	}
	t.Errorf("the failed run is not in the feed: %s", kinds(got))
}

// The runs that failed before this was fixed get an end too: the end of their
// last step, or when they were created if no step ever ran.
func TestTheRunsThatHadFailedGetAnEnd(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	t.Cleanup(func() { migrate(t, "up") })
	if err := postgres.MigrateDownTo(ctx, os.Getenv("BREVIS_TEST_DATABASE_URL"), beforeTheEnds); err != nil {
		t.Fatalf("migrate down to %d: %v", beforeTheEnds, err)
	}

	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	stepEnded := created.Add(3 * time.Minute)
	withSteps, noSteps := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{withSteps, noSteps} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO runs (id, workflow_slug, idempotency_key, status, definicao, criado_em)
			VALUES ($1, 'failures', $2, 'failed', '{}', $3)`, id, uuid.NewString(), created); err != nil {
			t.Fatal(err)
		}
	}
	for i, end := range []time.Time{created.Add(time.Minute), stepEnded} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO task_runs (id, run_id, node_id, status, attempt, terminado_em)
			VALUES ($1, $2, 'load', 'failed', $3, $4)`, uuid.New(), withSteps, i, end); err != nil {
			t.Fatal(err)
		}
	}

	migrate(t, "up")
	for id, want := range map[uuid.UUID]time.Time{withSteps: stepEnded, noSteps: created} {
		if got := endedAt(t, pool, id); got == nil || !got.Equal(want) {
			t.Errorf("run %s: terminado_em = %v, wanted %v", id, got, want)
		}
	}
}
