package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// waitingRun queues one run. claimed puts it in flight the way the claim does
// -- reivindicado_em set -- at `since`; later makes it unavailable until then.
func waitingRun(t *testing.T, pool *postgres.Pool, slug, group string, limit, priority int,
	claimed bool, since time.Time, later time.Duration) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: slug, IdempotencyKey: uuid.NewString(),
		Definition: []byte(`{}`), MaxActive: limit, Group: group,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	var claimedAt any
	if claimed {
		claimedAt = since
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO queue_items (run_id, prioridade, disponivel_em, reivindicado_em, reivindicado_por)
		VALUES ($1, $2, now() + $3::interval, $4, CASE WHEN $4::timestamptz IS NULL THEN NULL ELSE 'w' END)`,
		r.ID, priority, fmt.Sprintf("%d milliseconds", later.Milliseconds()), claimedAt); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

var tenFifteen = time.Date(2026, 10, 13, 10, 15, 0, 0, time.UTC)

// #95, the case #32 left without words: the daily, clicked during a slot of
// the */15 it shares a group with, waits -- and the page has to say on what.
func TestARunWaitingOnItsGroupNamesWhoHoldsIt(t *testing.T) {
	pool := insightsDB(t)
	holder := waitingRun(t, pool, "every_15", "jaffle_models", 1, 0, true, tenFifteen, 0)
	waiting := waitingRun(t, pool, "daily", "jaffle_models", 1, 10, false, time.Time{}, 0)

	w, err := postgres.NewRunRepo(pool).WaitingOn(context.Background(), waiting)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil {
		t.Fatal("a run behind a full group is not waiting on it")
	}
	if w.Group != "jaffle_models" || w.Limit != 1 || w.Ahead != 0 || len(w.Holders) != 1 {
		t.Fatalf("wait = %+v", w)
	}
	h := w.Holders[0]
	if h.RunID != holder || h.Workflow != "every_15" || !h.Since.Equal(tenFifteen) {
		t.Errorf("holder = %+v; wanted every_15 %s since %s", h, holder, tenFifteen)
	}
}

// With no group the workflow contends with itself, and that is what it waits on.
func TestARunWaitingOnItsOwnWorkflowsLimitSaysSo(t *testing.T) {
	pool := insightsDB(t)
	waitingRun(t, pool, "id_verification", "", 1, 0, true, tenFifteen, 0)
	waiting := waitingRun(t, pool, "id_verification", "", 1, 0, false, time.Time{}, 0)

	w, err := postgres.NewRunRepo(pool).WaitingOn(context.Background(), waiting)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil || w.Group != "" || w.Limit != 1 || len(w.Holders) != 1 || w.Holders[0].Workflow != "id_verification" {
		t.Errorf("wait = %+v; wanted its own workflow's limit, held by one run", w)
	}
}

// A free slot is not a free pass: the claim hands it to whoever is first, and
// a run with two ahead of it waits for them.
func TestARunBehindOthersOfItsGroupCountsThem(t *testing.T) {
	pool := insightsDB(t)
	waitingRun(t, pool, "a", "warehouse", 2, 0, true, tenFifteen, 0)
	waitingRun(t, pool, "b", "warehouse", 2, 5, false, time.Time{}, 0)
	waitingRun(t, pool, "c", "warehouse", 2, 5, false, time.Time{}, 0)
	waiting := waitingRun(t, pool, "d", "warehouse", 2, 0, false, time.Time{}, 0)

	w, err := postgres.NewRunRepo(pool).WaitingOn(context.Background(), waiting)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil || w.Ahead != 2 || len(w.Holders) != 1 {
		t.Errorf("wait = %+v; wanted 1 holder and 2 ahead", w)
	}
}

// Every case where the concurrency limit is NOT what holds the run says
// nothing: a wrong reason is worse than none.
func TestARunTheLimitDoesNotHoldHasNoWait(t *testing.T) {
	cases := map[string]func(t *testing.T, pool *postgres.Pool) uuid.UUID{
		"next in line": func(t *testing.T, pool *postgres.Pool) uuid.UUID {
			waitingRun(t, pool, "a", "warehouse", 2, 0, true, tenFifteen, 0)
			return waitingRun(t, pool, "b", "warehouse", 2, 0, false, time.Time{}, 0)
		},
		"no limit": func(t *testing.T, pool *postgres.Pool) uuid.UUID {
			waitingRun(t, pool, "a", "", 0, 0, true, tenFifteen, 0)
			return waitingRun(t, pool, "a", "", 0, 0, false, time.Time{}, 0)
		},
		"already claimed": func(t *testing.T, pool *postgres.Pool) uuid.UUID {
			return waitingRun(t, pool, "a", "g", 1, 0, true, tenFifteen, 0)
		},
		// A retry's backoff, with the slot free: the clock holds it, not the group.
		"not available yet": func(t *testing.T, pool *postgres.Pool) uuid.UUID {
			return waitingRun(t, pool, "a", "g", 1, 0, false, time.Time{}, time.Hour)
		},
		"not in the queue": func(t *testing.T, pool *postgres.Pool) uuid.UUID {
			r, err := postgres.NewRunRepo(pool).Create(context.Background(), dom.Run{
				WorkflowSlug: "a", IdempotencyKey: uuid.NewString(), Definition: []byte(`{}`), MaxActive: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			return r.ID
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			pool := insightsDB(t)
			id := setup(t, pool)
			w, err := postgres.NewRunRepo(pool).WaitingOn(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if w != nil {
				t.Errorf("wait = %+v; the limit does not hold this run", w)
			}
		})
	}
}

// The claim's one trap, again: a group called `daily` is not the workflow
// `daily`, and a run of one must not be told it waits on the other.
func TestAGroupAndAWorkflowOfTheSameNameAreNotOneWait(t *testing.T) {
	pool := insightsDB(t)
	waitingRun(t, pool, "daily", "", 1, 0, true, tenFifteen, 0)
	waiting := waitingRun(t, pool, "other", "daily", 1, 0, false, time.Time{}, 0)

	w, err := postgres.NewRunRepo(pool).WaitingOn(context.Background(), waiting)
	if err != nil {
		t.Fatal(err)
	}
	if w != nil {
		t.Errorf("the group daily was told it waits on the workflow daily: %+v", w)
	}
}
