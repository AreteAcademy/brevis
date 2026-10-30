package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// createdOn makes a run of slug created at that instant, with params.
func createdOn(t *testing.T, pool *postgres.Pool, slug string, at time.Time, params map[string]string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	r, err := postgres.NewRunRepo(pool).Create(ctx, dom.Run{
		WorkflowSlug: slug, IdempotencyKey: uuid.NewString(), TriggerType: "manual",
		Definition: []byte(`{}`), Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runs SET criado_em = $2 WHERE id = $1`, r.ID, at); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// A WORKFLOW'S HISTORY IS ITS RUNS, newest first, with the values each was
// given -- and a day is a UTC day, the calendar's.
func TestAWorkflowsHistoryCarriesParamsAndKeepsADay(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	late := createdOn(t, pool, "daily", day.Add(23*time.Hour+30*time.Minute), map[string]string{"full": "true"})
	early := createdOn(t, pool, "daily", day.Add(time.Minute), nil)
	createdOn(t, pool, "daily", day.Add(24*time.Hour), nil) // the next day
	createdOn(t, pool, "daily", day.Add(-time.Minute), nil) // the day before
	createdOn(t, pool, "other", day.Add(time.Hour), nil)    // another workflow

	repo := postgres.NewReadRepo(pool)
	got, err := repo.WorkflowHistory(ctx, "daily", &day, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != late.String() || got[1].ID != early.String() {
		t.Fatalf("the day's runs = %+v", got)
	}
	if got[0].Params["full"] != "true" {
		t.Errorf("a run's params = %v", got[0].Params)
	}

	all, err := repo.WorkflowHistory(ctx, "daily", nil, 3)
	if err != nil || len(all) != 3 {
		t.Errorf("the last three = %d (%v)", len(all), err)
	}
}
