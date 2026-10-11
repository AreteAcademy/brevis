package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

type landingRow struct {
	target      string
	rows, bytes *int64
	at          time.Time
	slug        string
	legacy      bool
}

func landingRows(t *testing.T, pool *postgres.Pool, runID uuid.UUID) map[string]landingRow {
	t.Helper()
	rs, err := pool.Query(context.Background(), `
		SELECT target, rows_written, bytes_written, loaded_at, workflow_slug, legacy
		FROM landings WHERE run_id = $1`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	out := map[string]landingRow{}
	for rs.Next() {
		var r landingRow
		if err := rs.Scan(&r.target, &r.rows, &r.bytes, &r.at, &r.slug, &r.legacy); err != nil {
			t.Fatal(err)
		}
		out[r.target] = r
	}
	return out
}

func n(v int64) *int64 { return &v }

func landingsDB(t *testing.T) *postgres.Pool {
	t.Helper()
	pool := loadDB(t)
	if _, err := pool.Exec(context.Background(), `TRUNCATE landings`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestRecordLandingsKeepsOneRowPerTargetAndAbsentStaysNull(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)
	at := time.Date(2026, 10, 7, 5, 41, 12, 0, time.UTC)

	err := postgres.NewRunRepo(pool).RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
		{Target: "bigquery://acme-prod/silver/orders", Rows: n(15), Bytes: n(900), At: at},
		{Target: "bigquery://acme-prod/silver/items", Rows: n(0), At: at},
		{Target: "s3://acme-archive/orders/", At: at},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := landingRows(t, pool, runID)
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(got), got)
	}
	if o := got["bigquery://acme-prod/silver/orders"]; *o.rows != 15 || *o.bytes != 900 || !o.at.Equal(at) || o.slug != "vendas" {
		t.Errorf("orders = %+v", o)
	}
	if i := got["bigquery://acme-prod/silver/items"]; i.rows == nil || *i.rows != 0 {
		t.Errorf("items rows = %v, want an explicit 0", i.rows)
	}
	if a := got["s3://acme-archive/orders/"]; a.rows != nil || a.bytes != nil {
		t.Errorf("archive = %+v, want NULL counts: the step did not say", a)
	}
}

// Attempt 2 replaces attempt 1's numbers for a target it lands again, and a
// target only attempt 1 landed stays: those rows are in the warehouse.
func TestARetryReplacesItsOwnNumbersAndKeepsWhatOnlyTheFirstAttemptLanded(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)
	first := time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)
	second := first.Add(10 * time.Minute)

	if err := repo.RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
		{Target: "postgres://analytics/public/orders", Rows: n(40000), At: first},
		{Target: "postgres://analytics/public/staging_orders", Rows: n(40000), At: first},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
		{Target: "postgres://analytics/public/orders", Rows: n(48000), At: second},
	}); err != nil {
		t.Fatal(err)
	}

	got := landingRows(t, pool, runID)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if o := got["postgres://analytics/public/orders"]; *o.rows != 48000 || !o.at.Equal(second) {
		t.Errorf("orders = %+v, want attempt 2's numbers", o)
	}
	if _, ok := got["postgres://analytics/public/staging_orders"]; !ok {
		t.Error("a target only the first attempt landed was lost")
	}
}

// The catalog outlives retention: purging a run must not take its tables off
// /data.
func TestPurgingARunKeepsItsLandings(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	runID := aged(t, pool, "vendas", 730*24*time.Hour, dom.StatusSuccess)

	if err := repo.RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
		{Target: "postgres://analytics/public/orders", Rows: n(10), At: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Prune(ctx,
		postgres.Retention{TrimAfter: time.Hour, PurgeAfter: 365 * 24 * time.Hour}, false); err != nil {
		t.Fatal(err)
	}
	if runExists(t, pool, runID) {
		t.Fatal("the run survived the purge; this test proves nothing")
	}
	if got := landingRows(t, pool, runID); len(got) != 1 {
		t.Fatalf("landings after the purge = %d, want 1", len(got))
	}
}

// A landing carries the configuration its pipeline described: stored once by
// its hash, however many landings and runs point at it.
func TestALandingKeepsTheConfigurationItWasWrittenWith(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE writer_configs CASCADE`); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewRunRepo(pool)
	cfg := &app.WriterConfig{Hash: "sha256:abc", Body: []byte(`[{"path":"target.dedup","value":"merge","default":false}]`)}

	for range 2 {
		runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)
		if err := repo.RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
			{Target: "bigquery://acme/silver/orders", Rows: n(1), At: time.Now(), Config: cfg},
			{Target: "bigquery://acme/silver/items", Rows: n(2), At: time.Now(), Config: cfg},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var configs, pointing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM writer_configs`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM landings WHERE config_hash = 'sha256:abc'`).Scan(&pointing); err != nil {
		t.Fatal(err)
	}
	if configs != 1 || pointing != 4 {
		t.Fatalf("configs = %d, landings pointing at it = %d; want 1 and 4", configs, pointing)
	}
}

// A step that did not describe itself lands with no configuration.
func TestALandingWithoutAConfigurationPointsAtNothing(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)
	if err := postgres.NewRunRepo(pool).RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
		{Target: "bigquery://acme/silver/orders", Rows: n(1), At: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	var hash *string
	if err := pool.QueryRow(ctx, `SELECT config_hash FROM landings WHERE run_id = $1`, runID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != nil {
		t.Fatalf("config_hash = %q, want NULL", *hash)
	}
}
