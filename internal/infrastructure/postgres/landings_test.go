package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// A step's configuration history is written as it CHANGES, beside the
// landings: one line per change, not one per landing -- so the destination's
// page reads twenty lines, not every landing since the step began.
func TestAStepsConfigurationHistoryGainsALineOnlyWhenItChanges(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE writer_configs, step_config_history CASCADE`); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewRunRepo(pool)
	a := &app.WriterConfig{Hash: "sha256:a", Body: []byte(`[{"path":"target.dedup","value":"none","default":false}]`)}
	b := &app.WriterConfig{Hash: "sha256:b", Body: []byte(`[{"path":"target.dedup","value":"merge","default":false}]`)}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	for i, cfg := range []*app.WriterConfig{a, a, a, b, b, a} {
		runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)
		if err := repo.RecordLandings(ctx, runID, dom.Step("carga"), "vendas", []app.Landing{
			{Target: "bigquery://acme/silver/orders", Rows: n(1), At: at.Add(time.Duration(i) * time.Hour), Config: cfg},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var hashes []string
	rows, err := pool.Query(ctx, `SELECT config_hash FROM step_config_history
		WHERE target = 'bigquery://acme/silver/orders' ORDER BY first_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	// a → b → a: three changes, and going back is a change too.
	if strings.Join(hashes, " ") != "sha256:a sha256:b sha256:a" {
		t.Fatalf("history = %v, want one line per change", hashes)
	}
}

// Mapped instances of one step land the same table in parallel, each in its
// own transaction, and each saw the step's "last line" from before the others
// committed -- so a new configuration was recorded once per instance. One
// line per change means one line, whoever lands first.
func TestParallelInstancesRecordAChangeOnce(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE writer_configs, step_config_history CASCADE`); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewRunRepo(pool)
	runID := aged(t, pool, "vendas", time.Hour, dom.StatusSuccess)
	const instances, rounds = 8, 5

	for round := range rounds {
		hash := fmt.Sprintf("sha256:round%d", round)
		cfg := &app.WriterConfig{Hash: hash, Body: []byte(`[]`)}
		// A configuration the engine has seen before -- a step going back to
		// it. A new one is first inserted into writer_configs, and that
		// unique key makes the instances wait for each other by accident;
		// a known one does not, and that is where they raced.
		if _, err := pool.Exec(ctx, `INSERT INTO writer_configs VALUES ($1, '[]', now())`, hash); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range instances {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := repo.RecordLandings(ctx, runID, dom.StepKey{Node: "carga", MapIndex: i}, "vendas", []app.Landing{
					{Target: "bigquery://acme/silver/orders", Rows: n(1), At: time.Now(), Config: cfg},
				})
				if err != nil {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()

		var lines int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM step_config_history WHERE config_hash = $1`, hash).Scan(&lines); err != nil {
			t.Fatal(err)
		}
		if lines != 1 {
			t.Fatalf("round %d: %d lines for one change of configuration, want 1", round, lines)
		}
	}
}

// A mapped step whose instances are configured apart -- one source file per
// partition, landing one table -- has nothing new to record on its next run.
// The history used to compare each instance with the step's LAST line, which
// was another instance's, and wrote a, b, c again every run.
func TestInstancesConfiguredApartRecordNothingOnAnUnchangedRun(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE writer_configs, step_config_history CASCADE`); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewRunRepo(pool)
	const target = "files://landing/"
	land := func(runID uuid.UUID, configs ...string) {
		t.Helper()
		for i, h := range configs {
			cfg := &app.WriterConfig{Hash: "sha256:" + h, Body: []byte(`[{"path":"source.path","value":"` + h + `"}]`)}
			if err := repo.RecordLandings(ctx, runID, dom.StepKey{Node: "load", MapIndex: i}, "daily_sales", []app.Landing{
				{Target: target, Rows: n(1), At: time.Now(), Config: cfg},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	lines := func() int {
		t.Helper()
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM step_config_history WHERE target = $1`, target).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	land(aged(t, pool, "daily_sales", 3*time.Hour, dom.StatusSuccess), "north", "south", "east")
	first := lines()
	land(aged(t, pool, "daily_sales", 2*time.Hour, dom.StatusSuccess), "north", "south", "east")
	if got := lines(); got != first {
		t.Fatalf("an unchanged run took the history from %d lines to %d", first, got)
	}
	// One instance really changing is one line.
	land(aged(t, pool, "daily_sales", time.Hour, dom.StatusSuccess), "north", "west", "east")
	if got := lines(); got != first+1 {
		t.Fatalf("one instance changing took the history from %d lines to %d, want %d", first, got, first+1)
	}
}
