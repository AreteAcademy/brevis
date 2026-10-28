package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// runsDB truncates NOTHING: runs is read by the scheduler's tests, which may be
// running at the same moment, and every test here makes a run of its own.
func runsDB(t *testing.T) (*postgres.Pool, *postgres.RunRepo) {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the run repository tests (make up)")
	}
	pool, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, postgres.NewRunRepo(pool)
}

func newRun(t *testing.T, repo *postgres.RunRepo) uuid.UUID {
	t.Helper()
	r, err := repo.Create(context.Background(), dom.Run{
		WorkflowSlug: "draining", IdempotencyKey: uuid.NewString(), Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func drainingSince(t *testing.T, pool *postgres.Pool, id uuid.UUID) *time.Time {
	t.Helper()
	var since *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT draining_since FROM runs WHERE id = $1`, id).Scan(&since); err != nil {
		t.Fatal(err)
	}
	return since
}

// The wait is measured from the FIRST time the run was sent back for a drain
// (#146): marking it again keeps that instant, or the ceiling would never be
// reached by a run that keeps being refused.
func TestMarkingADrainAgainKeepsTheFirstInstant(t *testing.T) {
	_, repo := runsDB(t)
	ctx := context.Background()
	id := newRun(t, repo)

	first, err := repo.MarkDraining(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if first > time.Second {
		t.Errorf("a first mark has waited %v, wanted about nothing", first)
	}
	time.Sleep(60 * time.Millisecond)

	again, err := repo.MarkDraining(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if again < 50*time.Millisecond {
		t.Errorf("marked again it has waited %v, wanted the time since the first mark", again)
	}
}

// Spending an attempt ends the wait: the next refusal starts a ceiling of its
// own. Written in the SAME transaction as the attempt, so the two cannot come
// apart.
func TestSpendingAnAttemptForgetsTheDrain(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	id := newRun(t, repo)
	if _, err := repo.MarkDraining(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, _, err := repo.Attempt(ctx, id, 3, nil); err != nil {
		t.Fatal(err)
	}
	if since := drainingSince(t, pool, id); since != nil {
		t.Errorf("draining_since = %v after an attempt, wanted NULL", since)
	}
}

// So does succeeding: a run that waited out a drain once and finished is not
// carrying that wait into anything after it.
func TestSucceedingForgetsTheDrain(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	id := newRun(t, repo)
	if _, err := repo.MarkDraining(ctx, id); err != nil {
		t.Fatal(err)
	}

	for _, to := range []dom.Status{dom.StatusQueued, dom.StatusRunning, dom.StatusSuccess} {
		if err := repo.Transicionar(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}
	if since := drainingSince(t, pool, id); since != nil {
		t.Errorf("draining_since = %v after success, wanted NULL", since)
	}
}

// And nothing else does: the trip a deferred run makes back to the queue goes
// through these, and forgetting there would restart the ceiling every time.
func TestGoingBackToTheQueueKeepsTheDrain(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	id := newRun(t, repo)
	for _, to := range []dom.Status{dom.StatusQueued, dom.StatusRunning} {
		if err := repo.Transicionar(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.MarkDraining(ctx, id); err != nil {
		t.Fatal(err)
	}

	for _, to := range []dom.Status{dom.StatusFailed, dom.StatusRetrying, dom.StatusQueued} {
		if err := repo.Transicionar(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}
	if since := drainingSince(t, pool, id); since == nil {
		t.Error("draining_since was forgotten on the way back to the queue")
	}
}
