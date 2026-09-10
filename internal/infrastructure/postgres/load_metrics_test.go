package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
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
func backfill(t *testing.T, pool *postgres.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO load_metrics (
    run_id, node_id, map_index, workflow_slug, em,
    linhas, registros, ignorados, bytes_saida, load_ms,
    bytes_entrada, paginas, tentativas, extract_ms)
SELECT t.run_id, t.node_id, t.map_index, r.workflow_slug, r.criado_em,
       COALESCE((carga->'numeros'->>'rows')::bigint, 0),
       COALESCE((carga->'numeros'->>'records')::bigint, 0),
       COALESCE((carga->'numeros'->>'ignored')::bigint, 0),
       COALESCE((carga->'numeros'->>'load_bytes')::bigint, 0),
       COALESCE((carga->>'ms')::bigint, 0),
       COALESCE((extracao->'numeros'->>'bytes')::bigint, 0),
       COALESCE((extracao->'numeros'->>'pages')::int, 0),
       COALESCE((extracao->'numeros'->>'http_attempts')::int, 0),
       COALESCE((extracao->>'ms')::bigint, 0)
FROM task_runs t
JOIN runs r ON r.id = t.run_id
LEFT JOIN LATERAL (
    SELECT e FROM jsonb_array_elements(t.etapas) e
    WHERE e->>'nome' = 'load' AND e->>'estado' = 'done'
    ORDER BY (e->>'indice')::int LIMIT 1
) l(carga) ON true
LEFT JOIN LATERAL (
    SELECT e FROM jsonb_array_elements(t.etapas) e
    WHERE e->>'nome' = 'extract' ORDER BY (e->>'indice')::int LIMIT 1
) x(extracao) ON true
WHERE carga IS NOT NULL
ON CONFLICT (run_id, node_id, map_index) DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
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
