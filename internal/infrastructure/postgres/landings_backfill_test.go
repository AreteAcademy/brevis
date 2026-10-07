package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/migrations"
)

// Migration 00013 recovers, from task_runs.etapas, the loads that happened
// before steps could say which table they wrote. What it can recover is the
// LABEL the load phase carried (`postgres:landing_orders`, `bronze.clicks`) and
// never a target -- turning a label into a URI would be inferring one -- so
// every row it writes is marked legacy.
//
// The rule for "which phase is a finished load" is the one 00012 and
// LoadNumbersFrom already share: by NAME, state done, and a scalar in `etapas`
// must not abort the migration. These cases are the ones that bit 00012.
var landingBackfillCases = []struct {
	name   string
	stages string
	want   *landingRow // nil: no row
}{
	{
		name:   "an ordinary finished load",
		stages: `[{"indice":0,"nome":"extract","estado":"done","em":"x"},{"indice":1,"nome":"load","estado":"done","em":"x","numeros":{"rows":48213,"detail":"postgres:landing.orders"}}]`,
		want:   &landingRow{target: "postgres:landing.orders", rows: n(48213), legacy: true},
	},
	{
		name:   "two map stages push load off position one",
		stages: `[{"indice":0,"nome":"extract","estado":"done","em":"x"},{"indice":1,"nome":"map","estado":"done","em":"x","numeros":{"detail":"not a load"}},{"indice":2,"nome":"map","estado":"done","em":"x"},{"indice":3,"nome":"load","estado":"done","em":"x","numeros":{"rows":99,"detail":"bronze.clicks"}}]`,
		want:   &landingRow{target: "bronze.clicks", rows: n(99), legacy: true},
	},
	{
		name:   "a finished load that did not say how many",
		stages: `[{"indice":0,"nome":"load","estado":"done","em":"x","numeros":{"detail":"s3://acme/landing/"}}]`,
		want:   &landingRow{target: "s3://acme/landing/", legacy: true},
	},
	{
		name:   "a load that failed",
		stages: `[{"indice":0,"nome":"load","estado":"failed","em":"x","numeros":{"rows":40000,"detail":"bronze.clicks"}}]`,
	},
	{
		name:   "a load still running when the process died",
		stages: `[{"indice":0,"nome":"load","estado":"running","em":"x"}]`,
	},
	{
		name:   "a finished load with no destination named",
		stages: `[{"indice":0,"nome":"load","estado":"done","em":"x","numeros":{"rows":5}}]`,
	},
	{
		name:   "a step that is not a pipeline",
		stages: `[]`,
	},
	{
		name:   "a scalar where the array should be",
		stages: `null`,
	},
}

// landingBackfillSQL cuts the backfill out of 00013, from `INSERT INTO landings`
// to the semicolon that ends it -- the migration's own SQL, never a copy.
func landingBackfillSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile("00013_landings.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	i := strings.Index(body, "INSERT INTO landings")
	if i < 0 {
		t.Fatal("00013 holds no INSERT INTO landings; this test is reading the wrong thing")
	}
	j := strings.Index(body[i:], ";")
	if j < 0 {
		t.Fatal("the backfill in 00013 does not end in a semicolon")
	}
	return body[i : i+j]
}

func TestTheLandingBackfillKeepsOnlyFinishedLoadsAndOnlyAsLegacy(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()

	for _, c := range landingBackfillCases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE landings, task_runs, runs CASCADE`); err != nil {
				t.Fatal(err)
			}
			runID := seedRunWithStages(t, pool, "vendas", c.stages)
			var created time.Time
			if err := pool.QueryRow(ctx, `SELECT criado_em FROM runs WHERE id = $1`, runID).Scan(&created); err != nil {
				t.Fatal(err)
			}

			if _, err := pool.Exec(ctx, landingBackfillSQL(t)); err != nil {
				t.Fatalf("the backfill failed: %v", err)
			}

			got := landingRows(t, pool, runID)
			if c.want == nil {
				if len(got) != 0 {
					t.Fatalf("backfilled %+v, want nothing", got)
				}
				return
			}
			r, ok := got[c.want.target]
			if !ok || len(got) != 1 {
				t.Fatalf("backfilled %+v, want one row for %q", got, c.want.target)
			}
			if !r.legacy {
				t.Error("a backfilled row must be legacy: it holds a label, not a target")
			}
			if (r.rows == nil) != (c.want.rows == nil) || (r.rows != nil && *r.rows != *c.want.rows) {
				t.Errorf("rows = %v, want %v", r.rows, c.want.rows)
			}
			if !r.at.Equal(created) {
				t.Errorf("loaded_at = %v, want the run's creation %v: nothing older says when the load ended", r.at, created)
			}
			if r.slug != "vendas" {
				t.Errorf("workflow = %q", r.slug)
			}
		})
	}
}

// A retried step has a task_run per attempt. The backfill keeps the LAST
// attempt that finished its load, the rule RecordLandings applies going
// forward: attempt 2 replaces attempt 1's numbers.
func TestTheLandingBackfillKeepsTheLastAttemptThatLanded(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE landings, task_runs, runs CASCADE`); err != nil {
		t.Fatal(err)
	}
	runID := seedRunWithStages(t, pool, "vendas",
		`[{"indice":0,"nome":"load","estado":"done","em":"x","numeros":{"rows":40000,"detail":"bronze.clicks"}}]`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, map_index, etapas)
		VALUES ($1, $2, 'carga', 'success', 1, -1, $3)`, uuid.New(), runID,
		`[{"indice":0,"nome":"load","estado":"done","em":"x","numeros":{"rows":48000,"detail":"bronze.clicks"}}]`); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, landingBackfillSQL(t)); err != nil {
		t.Fatal(err)
	}
	got := landingRows(t, pool, runID)
	if r := got["bronze.clicks"]; len(got) != 1 || r.rows == nil || *r.rows != 48000 {
		t.Fatalf("backfilled %+v, want one row with attempt 1's 48000", got)
	}
}

// What the runner already recorded is not touched: the backfill runs once, in
// the migration, but nothing about it may overwrite a real landing.
func TestTheLandingBackfillNeverOverwritesARealLanding(t *testing.T) {
	pool := landingsDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE landings, task_runs, runs CASCADE`); err != nil {
		t.Fatal(err)
	}
	runID := seedRunWithStages(t, pool, "vendas",
		`[{"indice":0,"nome":"load","estado":"done","em":"x","numeros":{"rows":1,"detail":"bronze.clicks"}}]`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO landings (run_id, node_id, map_index, target, workflow_slug, loaded_at, rows_written)
		VALUES ($1, 'carga', -1, 'bronze.clicks', 'vendas', now(), 7)`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, landingBackfillSQL(t)); err != nil {
		t.Fatal(err)
	}
	if r := landingRows(t, pool, runID)["bronze.clicks"]; r.legacy || *r.rows != 7 {
		t.Fatalf("the backfill overwrote a real landing: %+v", r)
	}
}
