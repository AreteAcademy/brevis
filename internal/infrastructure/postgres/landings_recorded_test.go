package postgres_test

import (
	"context"
	"testing"
	"time"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// THE TWO CLOCKS ARE DIFFERENT CLOCKS, and that is the whole reason this
// column exists.
//
// `loaded_at` is what the STEP said -- the `at` of its `landed` line, clamped
// five minutes into the future and not clamped against the past at all. A
// step reporting the instant its query began reports something true and
// something old. `recorded_at` is when the ENGINE wrote the row.
//
// A poll on `loaded_at` would never see this landing: it is dated an hour
// before any cursor a live scheduler holds.
func TestALandingIsRecordedWhenItArrivesAndNotWhenItSaysItHappened(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)

	stepSays := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	before := time.Now().Add(-time.Minute)

	err := postgres.NewRunRepo(pool).RecordLandings(ctx, runID, dom.Step("carga"), "vendas",
		[]app.Landing{{Target: "bigquery://acme-prod/silver/orders", Rows: n(3), At: stepSays}})
	if err != nil {
		t.Fatal(err)
	}

	var loaded, recorded time.Time
	if err := pool.QueryRow(ctx,
		`SELECT loaded_at, recorded_at FROM landings WHERE run_id = $1`, runID).
		Scan(&loaded, &recorded); err != nil {
		t.Fatal(err)
	}

	if !loaded.Equal(stepSays) {
		t.Errorf("loaded_at = %s, wanted what the step said (%s)", loaded, stepSays)
	}
	if recorded.Before(before) {
		t.Errorf("recorded_at = %s, which is before this test started -- it took "+
			"the step's clock instead of the engine's", recorded)
	}
	if !recorded.After(loaded) {
		t.Errorf("recorded_at (%s) is not after loaded_at (%s); they are the same clock", recorded, loaded)
	}
}

// AND A ROW FROM BEFORE THE COLUMN EXISTED keeps its own history rather than
// the deploy's instant.
//
// The migration backfills `recorded_at` from `loaded_at`, deliberately: now()
// for every old row would put a year of landings inside one second, and a
// window poll reading that would see a year of them arriving at once. This
// asserts the migration's own UPDATE, by writing a row the way the backfill
// in 00013 left them and reading what 00015 made of it.
func TestTheBackfillKeepsEachRowsOwnTime(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)

	old := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO landings (run_id, node_id, map_index, target, workflow_slug,
		                      loaded_at, recorded_at, legacy)
		VALUES ($1, 'carga', -1, 'postgres:landing.orders', 'vendas', $2, $2, true)`,
		runID, old); err != nil {
		t.Fatal(err)
	}

	var recorded time.Time
	if err := pool.QueryRow(ctx,
		`SELECT recorded_at FROM landings WHERE run_id = $1`, runID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if !recorded.Equal(old) {
		t.Errorf("recorded_at = %s, wanted the row's own %s", recorded, old)
	}
}
