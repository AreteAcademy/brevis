package scheduler_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
)

// enqueueIn queues n runs of a workflow in a concurrency group, at a priority.
func enqueueIn(t *testing.T, repo *postgres.RunRepo, q *queue.Queue,
	slug, group string, n, limit, priority int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		r, err := repo.Create(ctx, dom.Run{
			WorkflowSlug: slug, IdempotencyKey: fmt.Sprintf("%s-%d-%d", slug, priority, i),
			Definition: []byte(`{}`), MaxActive: limit, Group: group,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := q.Enqueue(ctx, r.ID, priority, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

// #32, as reported. A daily and a */15 build the same dbt models, each with
// `concurrency: 1`, and a manual run of the daily landed on a slot of the
// other: one dropped the `__dbt_tmp` table the other was reading.
//
// In one group with limit 1, ten free slots hand out ONE run -- the manual,
// which outranks scheduled work -- and nothing else leaves until it is done.
func TestTwoWorkflowsInAGroupNeverRunTogether(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	enqueueIn(t, repo, q, "every_15", "jaffle_models", 1, 1, 0)
	enqueueIn(t, repo, q, "daily", "jaffle_models", 1, 1, 0)
	enqueueIn(t, repo, q, "daily", "jaffle_models", 1, 1, 10) // the click

	first, err := q.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("claim handed out %d runs of one group with limit 1", len(first))
	}
	if first[0].Priority != 10 {
		t.Errorf("the run handed out has priority %d; the manual one (10) goes first", first[0].Priority)
	}

	// A second dispatcher, or the same one a cycle later: still nothing.
	if more, err := q.Claim(ctx, "w2", 10); err != nil || len(more) != 0 {
		t.Fatalf("with a run of the group in flight, claim handed out %d more (err %v)", len(more), err)
	}

	if err := q.Done(ctx, first[0].ID); err != nil {
		t.Fatal(err)
	}
	next, err := q.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 {
		t.Errorf("after the first finished, claim handed out %d; wanted 1", len(next))
	}
}

// A group's limit is a number, not a switch: three workflows sharing a limit
// of 2 have at most two runs in flight between them.
func TestAGroupOfThreeHoldsItsLimitOfTwo(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	for _, slug := range []string{"a", "b", "c"} {
		enqueueIn(t, repo, q, slug, "warehouse", 2, 2, 0)
	}

	items, err := q.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("claim handed out %d runs of a group with limit 2", len(items))
	}
}

// A full group holds back its own members and nobody else: the queue is
// shared, as TestAWorkflowAtItsLimitDoesNotBlockTheOthers says of one workflow.
func TestAFullGroupDoesNotBlockTheOthers(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	enqueueIn(t, repo, q, "daily", "jaffle_models", 3, 1, 0)
	enqueueIn(t, repo, q, "every_15", "jaffle_models", 3, 1, 0)
	enqueueIn(t, repo, q, "loose", "", 2, 1, 0) // its own limit, under its slug
	enqueueIn(t, repo, q, "unlimited", "", 3, 0, 0)

	items, err := q.Claim(context.Background(), "w", 20)
	if err != nil {
		t.Fatal(err)
	}
	// 1 from the group + 1 from loose + 3 unlimited.
	if len(items) != 5 {
		t.Errorf("claim handed out %d; wanted 5 (1 grouped, 1 loose, 3 unlimited)", len(items))
	}
}

// A group named like a workflow is still a group: the partition key must not
// let `daily` the group and `daily` the workflow count as one.
//
// It is the COALESCE's one trap. A workflow with no group partitions under
// its slug, so a group that happens to share the name would otherwise share
// the limit with it.
func TestAGroupDoesNotCollideWithAWorkflowOfTheSameName(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	enqueueIn(t, repo, q, "daily", "", 2, 1, 0)      // the workflow daily, no group
	enqueueIn(t, repo, q, "other", "daily", 2, 1, 0) // a group that happens to be called daily

	items, err := q.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("claim handed out %d; the workflow daily and the group daily are two limits", len(items))
	}
}
