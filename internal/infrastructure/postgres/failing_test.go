package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// declared writes a workflow into a project, the way publishing does.
func declared(t *testing.T, pool *postgres.Pool, project string, slugs ...string) {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, slug, name) VALUES (gen_random_uuid(), $1, $1)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, project).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, s := range slugs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO workflows (id, project_id, slug, name) VALUES (gen_random_uuid(), $1, $2, $2)`,
			id, s); err != nil {
			t.Fatal(err)
		}
	}
}

// history writes a workflow's runs, oldest first, an hour apart.
func history(t *testing.T, pool *postgres.Pool, slug string, states ...dom.Status) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, len(states))
	for i, s := range states {
		ids[i] = finished(t, pool, slug, s, time.Duration(len(states)-i)*time.Hour)
	}
	return ids
}

// THE STREAK IS HOW MANY TIMES IN A ROW IT FAILED: counted back from the
// newest finished run to the last success. A cancel says nothing about the
// workflow and is stepped over; a run still going has not finished.
func TestFailureStreakCountsBackToTheLastSuccess(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	history(t, pool, "daily", dom.StatusFailed, dom.StatusSuccess,
		dom.StatusFailed, dom.StatusCanceled, dom.StatusFailed, dom.StatusRunning, dom.StatusQueued)

	if n, err := repo.FailureStreak(ctx, "daily"); err != nil || n != 2 {
		t.Errorf("FailureStreak = %d, %v; want 2", n, err)
	}
	history(t, pool, "fine", dom.StatusFailed, dom.StatusSuccess)
	if n, err := repo.FailureStreak(ctx, "fine"); err != nil || n != 0 {
		t.Errorf("FailureStreak after a success = %d, %v; want 0", n, err)
	}
	history(t, pool, "never_passed", dom.StatusFailed, dom.StatusFailed, dom.StatusFailed)
	if n, err := repo.FailureStreak(ctx, "never_passed"); err != nil || n != 3 {
		t.Errorf("FailureStreak without any success = %d, %v; want 3", n, err)
	}
}

// THE WORKFLOWS FAILING NOW are those whose last finished run failed, worst
// first: the longest streak, then the most recent. A paused one still counts
// -- pausing a broken workflow does not fix it -- and one taken out of the
// project does not, though its runs stay as history.
func TestFailingWorkflowsAreTheOnesWhoseLastFinishedRunFailed(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	declared(t, pool, "sales", "long", "recent", "older", "recovered", "paused", "busy", "never_ran", "canceled_last")
	declared(t, pool, "copy", "long") // the same slug in another project is the same runs
	if _, err := pool.Exec(ctx, `
		INSERT INTO schedules (id, workflow_slug, cron, ativo) VALUES (gen_random_uuid(), 'paused', '0 4 * * *', false)`); err != nil {
		t.Fatal(err)
	}

	long := history(t, pool, "long", dom.StatusSuccess, dom.StatusFailed, dom.StatusFailed, dom.StatusFailed)
	recent := history(t, pool, "recent", dom.StatusFailed)
	finished(t, pool, "older", dom.StatusFailed, 5*time.Hour)
	history(t, pool, "recovered", dom.StatusFailed, dom.StatusSuccess)
	paused := history(t, pool, "paused", dom.StatusFailed, dom.StatusFailed)
	history(t, pool, "busy", dom.StatusSuccess, dom.StatusRunning)
	canceled := history(t, pool, "canceled_last", dom.StatusFailed, dom.StatusCanceled)
	history(t, pool, "removed", dom.StatusFailed, dom.StatusFailed, dom.StatusFailed, dom.StatusFailed)

	got, err := postgres.NewRunRepo(pool).FailingWorkflows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		slug   string
		run    uuid.UUID
		streak int
	}{
		{"long", long[3], 3},
		{"paused", paused[1], 2},
		{"recent", recent[0], 1},
		{"canceled_last", canceled[0], 1},
		{"older", uuid.Nil, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("FailingWorkflows = %+v; want %d workflows", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Slug != w.slug || g.Streak != w.streak || (w.run != uuid.Nil && g.RunID != w.run) || g.FailedAt.IsZero() {
			t.Errorf("#%d = %+v; want %s with streak %d", i, g, w.slug, w.streak)
		}
	}
}

// EVERY ATTEMPT CARRIES WHEN IT STARTED: the cause is the failure that
// started last, and a step that never started has no time.
func TestLogsDaRunCarriesWhenEachAttemptStarted(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	id := finished(t, pool, "daily", dom.StatusFailed, time.Hour)
	at := time.Date(2026, 10, 28, 3, 12, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, iniciado_em) VALUES
		  (gen_random_uuid(), $1, 'load', 'failed', 1, $2),
		  (gen_random_uuid(), $1, 'report', 'pending', 0, NULL)`, id, at); err != nil {
		t.Fatal(err)
	}
	logs, err := postgres.NewRunRepo(pool).LogsDaRun(ctx, id)
	if err != nil || len(logs) != 2 {
		t.Fatalf("LogsDaRun = %+v, %v", logs, err)
	}
	if !logs[0].StartedAt.Equal(at) || !logs[1].StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, %v; want %v and zero", logs[0].StartedAt, logs[1].StartedAt, at)
	}
}
