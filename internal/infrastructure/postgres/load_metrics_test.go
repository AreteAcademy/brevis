package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/migrations"
)

// The rule "which phases become a trend row" is written TWICE: once in Go, in
// stageCollector.LoadNumbers, for every run from now on; and once in SQL, in
// migration 00012's backfill, for the history that already exists. Nothing
// compiles both.
//
// That is the shape this repository keeps getting caught by, so the cases live
// in one table and both implementations are run against it. A disagreement here
// is a chart with a step in it on the day of the upgrade -- the same pipeline
// reading one way before the migration and another way after.
var loadCases = []struct {
	name    string
	stages  string // exactly what the collector writes into task_runs.etapas
	want    *app.LoadNumbers
	comment string
}{
	{
		name: "the ordinary pipeline",
		stages: `[
		 {"indice":0,"nome":"extract","estado":"done","em":"x","ms":31000,"numeros":{"pages":12,"bytes":5000000,"http_attempts":13}},
		 {"indice":1,"nome":"load","estado":"done","em":"x","ms":22000,"numeros":{"rows":48213,"records":48300,"load_bytes":900000,"ignored":7}}]`,
		want: &app.LoadNumbers{
			Rows: 48213, Records: 48300, Ignored: 7, BytesOut: 900000, LoadMs: 22000,
			BytesIn: 5000000, Pages: 12, HTTPAttempts: 13, ExtractMs: 31000,
		},
	},
	{
		name: "two map stages push load off position one",
		stages: `[
		 {"indice":0,"nome":"extract","estado":"done","em":"x","ms":40000,"numeros":{"bytes":9000000}},
		 {"indice":1,"nome":"map","estado":"done","em":"x"},
		 {"indice":2,"nome":"map","estado":"done","em":"x"},
		 {"indice":3,"nome":"load","estado":"done","em":"x","ms":50000,"numeros":{"rows":99}}]`,
		want:    &app.LoadNumbers{Rows: 99, LoadMs: 50000, BytesIn: 9000000, ExtractMs: 40000},
		comment: "reading a fixed slot would file a map's numbers as a load's",
	},
	{
		name: "a load that failed",
		stages: `[
		 {"indice":0,"nome":"extract","estado":"done","em":"x","ms":40000},
		 {"indice":1,"nome":"load","estado":"failed","em":"x","ms":900,"numeros":{"rows":40000}}]`,
		want:    nil,
		comment: "half of something that never happened reads as a dataset shrinking",
	},
	{
		name:    "a load still running when the process died",
		stages:  `[{"indice":1,"nome":"load","estado":"running","em":"x"}]`,
		want:    nil,
		comment: "the screen calls this aborted on read; the trend must not count it",
	},
	{
		name:    "a step that is not a pipeline",
		stages:  `[]`,
		want:    nil,
		comment: "most steps. A row of zeros from each would sink every average",
	},
	{
		name:    "a load with no extract phase",
		stages:  `[{"indice":0,"nome":"load","estado":"done","em":"x","ms":1200,"numeros":{"rows":5}}]`,
		want:    &app.LoadNumbers{Rows: 5, LoadMs: 1200},
		comment: "a source already in memory announces no extract; the load still counts",
	},
}

func loadDB(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the load-metrics tests (make up)")
	}
	p, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := p.Exec(context.Background(),
		`TRUNCATE load_metrics, alertas, queue_items, task_runs, runs, schedules, workflows, projects CASCADE`); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestTheBackfillAgreesWithTheRunner.
//
// The same `etapas` through both paths: the SQL of the migration, and the Go
// the runner runs. Where they disagree, the upgrade puts a step in every chart.
func TestTheBackfillAgreesWithTheRunner(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()

	for _, c := range loadCases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE load_metrics, task_runs, runs CASCADE`); err != nil {
				t.Fatal(err)
			}
			runID := seedRunWithStages(t, pool, "vendas", c.stages)

			// (1) The SQL half: exactly the backfill of migration 00012.
			backfill(t, pool)
			fromSQL := readLoadRow(t, pool, runID)

			// (2) The Go half: the collector, fed the same phases.
			fromGo := runnerReading(t, c.stages)

			if c.want == nil {
				if fromSQL != nil {
					t.Errorf("the backfill kept a row it should not have: %+v", fromSQL)
				}
				if fromGo != nil {
					t.Errorf("the runner kept a row it should not have: %+v", fromGo)
				}
				return
			}
			if fromSQL == nil {
				t.Fatalf("the backfill dropped a row it should have kept")
			}
			if fromGo == nil {
				t.Fatalf("the runner dropped a row it should have kept")
			}
			if *fromSQL != *c.want {
				t.Errorf("backfill got %+v\n         want %+v", *fromSQL, *c.want)
			}
			if *fromGo != *c.want {
				t.Errorf("runner   got %+v\n         want %+v", *fromGo, *c.want)
			}
		})
	}
}

// And the write path itself: RecordLoad puts the row where LoadTrend reads it,
// stamped with the RUN's clock and not with now().
func TestRecordLoadIsReadBackByTheTrend(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	reads := postgres.NewReadRepo(pool)

	runID := seedRunWithStages(t, pool, "vendas", `[]`)
	// The run happened three days ago. The trend groups by the RUN's day, so a
	// row stamped now() would land in today's bucket and the chart would show
	// work on a day nothing ran.
	if _, err := pool.Exec(ctx,
		`UPDATE runs SET criado_em = now() - interval '3 days' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}

	n := app.LoadNumbers{
		Rows: 1000, Records: 1100, Ignored: 3, BytesOut: 64000, LoadMs: 2000,
		BytesIn: 128000, Pages: 4, HTTPAttempts: 5, ExtractMs: 8000,
	}
	step := dom.StepKey{Node: "carga", MapIndex: dom.Unmapped}
	if err := repo.RecordLoad(ctx, runID, step, "vendas", n); err != nil {
		t.Fatal(err)
	}

	days, err := reads.LoadTrend(ctx, "vendas", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("the trend has %d days, wanted 1: %+v", len(days), days)
	}
	d := days[0]
	if want := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02"); d.Date.Format("2006-01-02") != want {
		t.Errorf("the row landed on %s, not on the run's day %s",
			d.Date.Format("2006-01-02"), want)
	}
	if d.Rows != 1000 || d.BytesOut != 64000 || d.LoadMs != 2000 || d.Runs != 1 {
		t.Errorf("read back %+v", d)
	}
	// The derived numbers the screen actually draws.
	if got := d.BytesPerRow(); got != 64 {
		t.Errorf("bytes per row = %d, wanted 64", got)
	}
	if got := d.RowsPerSecond(); got != 500 {
		t.Errorf("rows per second = %d, wanted 500", got)
	}
}

// A retry OVERWRITES rather than adding. A step that failed after loading
// 40,000 rows and then succeeded loading 48,000 loaded 48,000; summing both
// would invent 88,000 that never existed.
func TestARetryOverwritesItsOwnRow(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	reads := postgres.NewReadRepo(pool)

	runID := seedRunWithStages(t, pool, "vendas", `[]`)
	step := dom.StepKey{Node: "carga", MapIndex: dom.Unmapped}

	if err := repo.RecordLoad(ctx, runID, step, "vendas", app.LoadNumbers{Rows: 40000}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordLoad(ctx, runID, step, "vendas", app.LoadNumbers{Rows: 48000}); err != nil {
		t.Fatal(err)
	}

	days, err := reads.LoadTrend(ctx, "vendas", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Rows != 48000 || days[0].Runs != 1 {
		t.Errorf("a retry did not overwrite: %+v", days)
	}
}

// A mapped step is one row per instance, and the day's total is their SUM: a
// fan-out over four regions loaded what all four loaded.
func TestAMappedStepContributesOneRowPerInstance(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	reads := postgres.NewReadRepo(pool)

	runID := seedRunWithStages(t, pool, "vendas", `[]`)
	for i := 0; i < 4; i++ {
		step := dom.StepKey{Node: "carga", MapIndex: i}
		if err := repo.RecordLoad(ctx, runID, step, "vendas", app.LoadNumbers{Rows: 1000}); err != nil {
			t.Fatal(err)
		}
	}

	days, err := reads.LoadTrend(ctx, "vendas", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Rows != 4000 || days[0].Runs != 4 {
		t.Errorf("the four instances did not add up: %+v", days)
	}
}

// The trend OUTLIVES the run, and that is the whole reason this table has no
// foreign key.
//
// This test asserted the opposite for a day, because the table shipped with an
// ON DELETE CASCADE that seemed tidy and was wrong: retention purges old runs,
// and a cascade would shorten every chart by exactly as much on the morning of
// the purge. The log of a run from March is read by nobody; how much that
// pipeline loaded in March is the one thing the trend is for.
func TestTheTrendOutlivesTheRun(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	reads := postgres.NewReadRepo(pool)

	runID := seedRunWithStages(t, pool, "vendas", `[]`)
	step := dom.StepKey{Node: "carga", MapIndex: dom.Unmapped}
	if err := repo.RecordLoad(ctx, runID, step, "vendas", app.LoadNumbers{Rows: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM runs WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	days, err := reads.LoadTrend(ctx, "vendas", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Rows != 7 {
		t.Errorf("the trend died with the run it summarised: %+v", days)
	}
}

// seedRunWithStages writes a run and one task_run carrying these phases.
func seedRunWithStages(t *testing.T, pool *postgres.Pool, slug, stages string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: slug, IdempotencyKey: uuid.NewString(),
		TriggerType: "schedule", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, map_index, etapas)
		VALUES ($1, $2, 'carga', 'success', 0, -1, $3)`,
		uuid.New(), r.ID, stages); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// backfill runs migration 00012's INSERT verbatim.
//
// Copied rather than re-derived, because a paraphrase would test a query that
// no installation ever ran. When the migration changes, this fails, and the
// failure is the reminder.
// backfill runs THE MIGRATION'S OWN SQL, read out of the embedded file.
//
// It used to hold a copy, pasted from 00012. That is the very duplication this
// file's header warns about, and it cost exactly what it was warning of: the
// migration aborted on any database with a scalar in `etapas`
// (`cannot extract elements from a scalar`) while this test stayed green,
// because the copy was never the thing that ran.
//
// Reading the file means a test that passes is a statement about the migration.
func backfill(t *testing.T, pool *postgres.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), backfillSQL(t)); err != nil {
		t.Fatal(err)
	}
}

// backfillSQL cuts the backfill out of the migration: from `INSERT INTO
// load_metrics` to the semicolon that ends it.
func backfillSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile("00012_metricas_de_carga.sql")
	if err != nil {
		t.Fatalf("reading the migration: %v", err)
	}
	body := string(raw)

	i := strings.Index(body, "INSERT INTO load_metrics")
	if i < 0 {
		t.Fatal("00012 no longer holds an INSERT INTO load_metrics; this test is reading the wrong thing")
	}
	j := strings.Index(body[i:], ";")
	if j < 0 {
		t.Fatal("the backfill in 00012 does not end in a semicolon")
	}
	return body[i : i+j]
}

// readLoadRow reads back what either path stored, or nil when it stored nothing.
func readLoadRow(t *testing.T, pool *postgres.Pool, runID uuid.UUID) *app.LoadNumbers {
	t.Helper()
	var n app.LoadNumbers
	err := pool.QueryRow(context.Background(), `
		SELECT linhas, registros, ignorados, bytes_saida, load_ms,
		       bytes_entrada, paginas, tentativas, extract_ms
		FROM load_metrics WHERE run_id = $1`, runID).
		Scan(&n.Rows, &n.Records, &n.Ignored, &n.BytesOut, &n.LoadMs,
			&n.BytesIn, &n.Pages, &n.HTTPAttempts, &n.ExtractMs)
	if err != nil {
		return nil
	}
	return &n
}

// runnerReading is what the runner would store for these phases.
func runnerReading(t *testing.T, stages string) *app.LoadNumbers {
	t.Helper()
	// Unmarshalled from the SAME bytes the database holds, so the Go path reads
	// what the SQL path reads and not a re-typed approximation of it.
	var recorded []app.RecordedStage
	if err := json.Unmarshal([]byte(stages), &recorded); err != nil {
		t.Fatal(err)
	}

	n, ok := app.LoadNumbersFrom(recorded)
	if !ok {
		return nil
	}
	return &n
}

// `etapas` is jsonb, and jsonb accepts a SCALAR. The backfill reads it with
// jsonb_array_elements, which does not: one row holding 'null'::jsonb aborts
// the MIGRATION -- not the row, the migration -- with `cannot extract elements
// from a scalar`, leaving load_metrics created and empty.
//
// It reached production. Every database with history has such a row: a task_run
// from before the stages existed, or one whose step died before writing any.
// The migration was only ever run against an empty database, where the backfill
// reads nothing and the column's shape never comes up.
//
// So the case is not "a scalar is skipped". It is "a scalar does not take the
// rows beside it down with it".
func TestAScalarInEtapasDoesNotAbortTheBackfill(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `TRUNCATE load_metrics, task_runs, runs CASCADE`); err != nil {
		t.Fatal(err)
	}

	runID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO runs (id, workflow_slug, idempotency_key, status, definicao, criado_em)
		VALUES ($1, 'vendas', $2, 'done', '{}'::jsonb, now())`,
		runID, runID.String()); err != nil {
		t.Fatal(err)
	}

	// Every shape a real column holds, with one good row among them. The good
	// row is the assertion: a guard that skipped everything would pass a test
	// that only checked for the absence of an error.
	//
	// SQL NULL is absent on purpose -- the column is NOT NULL, so the hazard is
	// only ever a jsonb scalar, and a case that cannot exist is not a case.
	for _, row := range []struct{ node, etapas string }{
		{"scalar_null", `'null'::jsonb`},
		{"scalar_number", `'7'::jsonb`},
		{"scalar_string", `'"done"'::jsonb`},
		{"scalar_object", `'{"nome":"load"}'::jsonb`},
		{"empty_array", `'[]'::jsonb`},
		{"good", `'[{"indice":1,"nome":"load","estado":"done","ms":22000,"numeros":{"rows":48213}}]'::jsonb`},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO task_runs (id, run_id, node_id, status, attempt, map_index, etapas)
			VALUES ($1, $2, $3, 'done', 1, -1, `+row.etapas+`)`,
			uuid.New(), runID, row.node); err != nil {
			t.Fatalf("seeding %s: %v", row.node, err)
		}
	}

	// The migration's own SQL, read from the file.
	if _, err := pool.Exec(ctx, backfillSQL(t)); err != nil {
		t.Fatalf("the backfill aborted on a column it has to tolerate: %v", err)
	}

	var nodes []string
	rows, err := pool.Query(ctx, `SELECT node_id FROM load_metrics ORDER BY node_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}

	if len(nodes) != 1 || nodes[0] != "good" {
		t.Errorf("backfilled %v; only the row with a real load stage belongs there", nodes)
	}

	var linhas int64
	if err := pool.QueryRow(ctx,
		`SELECT linhas FROM load_metrics WHERE node_id = 'good'`).Scan(&linhas); err != nil {
		t.Fatal(err)
	}
	if linhas != 48213 {
		t.Errorf("the surviving row reads %d rows, so the guard ate its numbers", linhas)
	}
}
