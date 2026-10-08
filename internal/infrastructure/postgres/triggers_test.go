package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// A SUBSCRIPTION IS READ FROM THE PUBLISHED DOCUMENT, and only from there.
//
// `definicao` is what `publish` stored, so the trigger the scheduler acts on
// is the trigger in the file that was published -- not a column somebody
// could update out from under it. A workflow with no trigger is not returned
// at all; the poll must not walk every workflow to find the two that have one.
func TestOnlyWorkflowsWithATriggerAreSubscriptions(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)

	publishWith(t, pool, "plain", wf.Trigger{})
	publishWith(t, pool, "orders", wf.Trigger{
		OnLanded: []string{"bigquery://acme-prod/bronze/orders"},
		Debounce: 5 * time.Minute,
	})
	publishWith(t, pool, "bronze_all", wf.Trigger{
		OnLanded: []string{"bigquery://acme-prod/bronze/*"},
	})

	got, err := repo.Subscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d subscriptions, wanted 2: %+v", len(got), got)
	}
	by := map[string]postgres.Subscription{}
	for _, s := range got {
		by[s.Slug] = s
	}
	if s := by["orders"]; s.Trigger.Debounce != 5*time.Minute || len(s.Trigger.OnLanded) != 1 {
		t.Errorf("orders = %+v", s)
	}
	if _, ok := by["plain"]; ok {
		t.Error("a workflow with no trigger came back as a subscription")
	}
}

// THE POLL READS BY recorded_at AND NEVER BY loaded_at, which is the whole
// reason 00015 exists. A landing the step dated an hour ago is still new to
// the engine, and a poll that missed it would miss every step with a clock
// that reports when its query began.
func TestLandingsSinceReadsTheEnginesClock(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)

	before := time.Now().Add(-time.Second)
	old := time.Now().Add(-2 * time.Hour).UTC()
	if err := repo.RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
		{Target: "bigquery://acme-prod/bronze/orders", Rows: n(3), At: old},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.LandingsSince(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d landings since a second ago, wanted 1 -- it read loaded_at", len(got))
	}
	if got[0].Target != "bigquery://acme-prod/bronze/orders" || got[0].Workflow != "vendas" {
		t.Errorf("landing = %+v", got[0])
	}
	// And the instant it carries is the one a debounce window is cut from.
	if !got[0].LoadedAt.Equal(old.Truncate(time.Microsecond)) {
		t.Errorf("loaded_at = %s, wanted %s", got[0].LoadedAt, old)
	}

	// Nothing recorded after this moment.
	later, err := repo.LandingsSince(ctx, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 0 {
		t.Errorf("%d landings from the future", len(later))
	}
}

// THE CURSOR IS PLANTED, NEVER ASSUMED. A first read with no row returns
// "not set", and the caller plants now() -- the rule schedules.ultimo_slot
// learned by leaving eighteen workflows without an automatic run.
func TestTheCursorStartsUnsetAndIsPlanted(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	if _, err := pool.Exec(ctx, `DELETE FROM landing_cursor`); err != nil {
		t.Fatal(err)
	}

	_, ok, err := repo.LandingCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a cursor existed before anything planted one")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.AdvanceLandingCursor(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.LandingCursor(ctx)
	if err != nil || !ok {
		t.Fatalf("after planting: %v %v", ok, err)
	}
	if !got.Equal(now) {
		t.Errorf("cursor = %s, wanted %s", got, now)
	}

	// AND IT ONLY EVER MOVES FORWARD. A cycle that read less far than the
	// last one must not rewind it -- that would replay a window already
	// seen, and the only reason it would be harmless is the idempotency key.
	// Relying on that for correctness rather than for safety is how a bug
	// becomes invisible.
	if err := repo.AdvanceLandingCursor(ctx, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _, err = repo.LandingCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Errorf("the cursor went backwards to %s", got)
	}
}

// publishWith stores a workflow document carrying a trigger, the way publish
// does -- through the domain's own JSON, so the test cannot pass on a shape
// the engine does not actually write.
func publishWith(t *testing.T, pool *postgres.Pool, slug string, tr wf.Trigger) {
	t.Helper()
	doc, err := json.Marshal(wf.Workflow{
		Slug: slug, Name: slug, Nodes: []wf.Node{{ID: "a", Run: "echo"}}, Trigger: tr,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var projeto string
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, slug, name) VALUES (gen_random_uuid(), 'it', 'it')
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflows (id, project_id, slug, name, definicao)
		VALUES (gen_random_uuid(), $1, $2, $2, $3)
		ON CONFLICT (project_id, slug) DO UPDATE SET definicao = EXCLUDED.definicao`,
		projeto, slug, doc); err != nil {
		t.Fatal(err)
	}
}
