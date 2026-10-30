package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// taskRun records one attempt of a step, started at `at`, taking `took`.
func taskRun(t *testing.T, pool *postgres.Pool, run uuid.UUID, node, status string, attempt int, at time.Time, took time.Duration) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, iniciado_em, terminado_em)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)`, run, node, status, attempt, at, at.Add(took)); err != nil {
		t.Fatal(err)
	}
}

// EACH STEP'S LAST TIME: the attempt of the newest run that ran it -- a step
// skipped by the last run keeps what it did before -- with how long it took.
func TestEachStepsLastRunIsTheNewestThatRanIt(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	old := createdOn(t, pool, "daily", t0, nil)
	taskRun(t, pool, old, "fetch", "success", 0, t0, 2*time.Second)
	taskRun(t, pool, old, "load", "success", 0, t0, 3*time.Second)
	recent := createdOn(t, pool, "daily", t0.Add(24*time.Hour), nil)
	taskRun(t, pool, recent, "fetch", "failed", 0, t0.Add(24*time.Hour), time.Second)
	taskRun(t, pool, recent, "fetch", "success", 1, t0.Add(24*time.Hour+time.Minute), 4*time.Second)
	other := createdOn(t, pool, "other", t0.Add(48*time.Hour), nil)
	taskRun(t, pool, other, "fetch", "failed", 0, t0.Add(48*time.Hour), time.Second)

	got, err := postgres.NewReadRepo(pool).LastStepRuns(ctx, "daily")
	if err != nil {
		t.Fatal(err)
	}
	if f := got["fetch"]; f.Status != "success" || f.DurationMs != 4000 {
		t.Errorf("fetch = %+v, want the newest run's last attempt: success in 4s", f)
	}
	if l := got["load"]; l.Status != "success" || l.DurationMs != 3000 {
		t.Errorf("load = %+v, want what it did before the newest run skipped it", l)
	}
	if len(got) != 2 {
		t.Errorf("steps = %v", got)
	}
}

// WHAT EACH STEP WROTE, recently, by scheme.
func TestStepConnectsIsWhatEachStepWrote(t *testing.T) {
	pool := insightsDB(t)
	now := time.Now()
	landAt(t, pool, "daily", "load", "bigquery://acme/bronze/orders", now.Add(-time.Hour), 0, n(1))
	landAt(t, pool, "daily", "export", "gs://acme/orders.csv", now.Add(-time.Hour), 0, n(1))
	landAt(t, pool, "daily", "load", "postgres://w/public/old", now.Add(-200*24*time.Hour), 0, n(1))
	got, err := postgres.NewReadRepo(pool).StepConnects(context.Background(), "daily", now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["load"], []string{"bigquery"}) || !slices.Equal(got["export"], []string{"gs"}) {
		t.Errorf("step connects = %v", got)
	}
}
