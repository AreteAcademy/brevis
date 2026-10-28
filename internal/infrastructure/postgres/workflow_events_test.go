package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func project(t *testing.T, pool *postgres.Pool, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO projects (id, slug, name) VALUES (gen_random_uuid(), $1, $1) RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func daily(cron, step string) wf.Workflow {
	return wf.Workflow{Slug: "daily", Name: "daily", Kind: wf.KindChain, Schedule: cron,
		Nodes: []wf.Node{{ID: "load", Run: step}}}
}

// details is every event's detail, oldest first.
func details(t *testing.T, pool *postgres.Pool) []map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT detail FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var d map[string]any
		if err := rows.Scan(&raw); err != nil || json.Unmarshal(raw, &d) != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// A PUBLISH IS IN THE FEED WHEN IT CHANGED SOMETHING: a new workflow, a new
// graph, a new schedule. A CI that republishes everything on every deploy
// must not fill the feed with publishes that changed nothing.
func TestPublishingAWorkflowIsRecordedOnlyWhenItChanges(t *testing.T) {
	pool := insightsDB(t)
	sales := project(t, pool, "sales")
	repo := postgres.NewWorkflowRepo(pool)
	ctx := context.Background()

	for _, w := range []wf.Workflow{
		daily("0 4 * * *", "load v1"), // new
		daily("0 4 * * *", "load v1"), // the same
		daily("0 4 * * *", "load v2"), // the graph changed
		daily("0 5 * * *", "load v2"), // the schedule changed
		daily("", "load v2"),          // the schedule went away
		daily("", "load v2"),          // the same
	} {
		if err := repo.Publicar(ctx, w, sales, "cli"); err != nil {
			t.Fatal(err)
		}
	}
	if got := recorded(t, pool); got != "workflow.published daily cli, workflow.published daily cli, workflow.published daily cli, workflow.published daily cli" {
		t.Fatalf("events = %s", got)
	}
	want := []map[string]any{
		{"project": "sales", "new": true, "schedule_changed": true},
		{"project": "sales", "new": false, "schedule_changed": false},
		{"project": "sales", "new": false, "schedule_changed": true},
		{"project": "sales", "new": false, "schedule_changed": true},
	}
	for i, d := range details(t, pool) {
		for k, v := range want[i] {
			if d[k] != v {
				t.Errorf("event %d: detail %v; want %v", i, d, want[i])
				break
			}
		}
		if len(d) != len(want[i]) {
			t.Errorf("event %d: detail %v has keys the spec does not", i, d)
		}
	}
}

// A PUBLISH THAT FAILS leaves neither the workflow nor its event: they are
// one transaction.
func TestAFailedPublishLeavesNoEvent(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	sales := project(t, pool, "sales")
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION refuse_schedule() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'the schedule is refused'; END $$;
		CREATE TRIGGER refuse BEFORE INSERT OR UPDATE ON schedules
		FOR EACH ROW EXECUTE FUNCTION refuse_schedule()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP TRIGGER refuse ON schedules; DROP FUNCTION refuse_schedule()`)
	})

	if err := postgres.NewWorkflowRepo(pool).Publicar(ctx, daily("0 4 * * *", "load"), sales, "cli"); err == nil {
		t.Fatal("the publish did not fail")
	}
	if got := recorded(t, pool); got != "" {
		t.Errorf("events = %s; want none", got)
	}
}

// A WORKFLOW TAKEN OUT OF ITS PROJECT is in the feed, one event per slug.
func TestPruningRecordsEachWorkflowRemoved(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	sales := project(t, pool, "sales")
	repo := postgres.NewWorkflowRepo(pool)
	for _, slug := range []string{"keep", "old_a", "old_b"} {
		w := daily("", "load")
		w.Slug, w.Name = slug, slug
		if err := repo.Publicar(ctx, w, sales, "cli"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `TRUNCATE events`); err != nil {
		t.Fatal(err)
	}

	removed, err := repo.Podar(ctx, sales, []string{"keep"}, "cli")
	if err != nil || len(removed) != 2 {
		t.Fatalf("Podar = %v, %v", removed, err)
	}
	if got := recorded(t, pool); got != "workflow.removed old_a cli, workflow.removed old_b cli" &&
		got != "workflow.removed old_b cli, workflow.removed old_a cli" {
		t.Errorf("events = %s", got)
	}
	for _, d := range details(t, pool) {
		if d["project"] != "sales" || len(d) != 1 {
			t.Errorf("detail = %v; want only the project", d)
		}
	}
}
