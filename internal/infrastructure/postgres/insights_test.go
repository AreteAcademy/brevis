package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/notify"
)

func insightsDB(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the insights tests (make up)")
	}
	p, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := p.Exec(context.Background(),
		`TRUNCATE alertas, queue_items, task_runs, runs, schedules, workflows, projects CASCADE`); err != nil {
		t.Fatal(err)
	}
	return p
}

// finished writes a run that started and ended, the way a real one does.
func finished(t *testing.T, pool *postgres.Pool, slug string, status dom.Status, took time.Duration) uuid.UUID {
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
	// The timestamps are set directly: Transicionar writes now(), and a test
	// about durations needs to choose them.
	if _, err := pool.Exec(ctx, `
		UPDATE runs SET status = $2, criado_em = now() - $3::interval, terminado_em = now()
		WHERE id = $1`, r.ID, status, took.String()); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func withStages(t *testing.T, pool *postgres.Pool, runID uuid.UUID, node string,
	status dom.Status, numbers map[string]any,
) {
	t.Helper()
	ctx := context.Background()
	stages, err := json.Marshal([]map[string]any{{
		"indice": 0, "nome": "load", "estado": "done", "numeros": numbers,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, etapas)
		VALUES ($1, $2, $3, $4, 0, $5)`,
		uuid.New(), runID, node, status, stages); err != nil {
		t.Fatal(err)
	}
}

func window(t *testing.T, pool *postgres.Pool) notify.Report {
	t.Helper()
	to := time.Now().Add(time.Minute)
	rep, err := postgres.NewReadRepo(pool).Insights(context.Background(),
		to.Add(-24*time.Hour), to, "test")
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func find(t *testing.T, r notify.Report, slug string) notify.WorkflowInsight {
	t.Helper()
	for _, w := range r.Workflows {
		if w.Slug == slug {
			return w
		}
	}
	t.Fatalf("%q is not in the report: %+v", slug, r.Workflows)
	return notify.WorkflowInsight{}
}

func TestTheWindowCountsWhatHappenedInIt(t *testing.T) {
	pool := insightsDB(t)
	finished(t, pool, "daily_sales", dom.StatusSuccess, 2*time.Minute)
	finished(t, pool, "daily_sales", dom.StatusSuccess, 4*time.Minute)
	finished(t, pool, "daily_sales", dom.StatusFailed, 30*time.Second)
	finished(t, pool, "id_verification", dom.StatusSuccess, time.Minute)

	rep := window(t, pool)
	if rep.Runs != 4 || rep.Succeeded != 3 || rep.Failed != 1 {
		t.Fatalf("runs=%d succeeded=%d failed=%d", rep.Runs, rep.Succeeded, rep.Failed)
	}
	w := find(t, rep, "daily_sales")
	if w.Runs != 3 || w.Failed != 1 {
		t.Errorf("daily_sales: runs=%d failed=%d", w.Runs, w.Failed)
	}
	// min 30s, max 4m: the window has to preserve both ends, because the
	// maximum is the run that nearly did not finish and the average hides it.
	if w.Min < 25*time.Second || w.Min > 40*time.Second {
		t.Errorf("min = %s, wanted about 30s", w.Min)
	}
	if w.Max < 3*time.Minute+50*time.Second || w.Max > 4*time.Minute+10*time.Second {
		t.Errorf("max = %s, wanted about 4m", w.Max)
	}
}

// A run OUTSIDE the window is not in the report. It is the assertion that makes
// "this week" mean this week.
func TestARunOutsideTheWindowIsNotCounted(t *testing.T) {
	pool := insightsDB(t)
	old := finished(t, pool, "daily_sales", dom.StatusSuccess, time.Minute)
	if _, err := pool.Exec(context.Background(),
		`UPDATE runs SET criado_em = now() - interval '40 days' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	finished(t, pool, "daily_sales", dom.StatusSuccess, time.Minute)

	if rep := window(t, pool); rep.Runs != 1 {
		t.Errorf("runs = %d; a run from forty days ago is in this week's report", rep.Runs)
	}
}

// A run still going has no duration, and counting it as zero would drag every
// average down at the exact moment something is stuck.
func TestARunStillGoingDoesNotEnterTheAverage(t *testing.T) {
	pool := insightsDB(t)
	finished(t, pool, "daily_sales", dom.StatusSuccess, 10*time.Minute)

	repo := postgres.NewRunRepo(pool)
	stuck, err := repo.Create(context.Background(), dom.Run{
		WorkflowSlug: "daily_sales", IdempotencyKey: "stuck", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE runs SET status = 'running', terminado_em = NULL WHERE id = $1`, stuck.ID); err != nil {
		t.Fatal(err)
	}

	w := find(t, window(t, pool), "daily_sales")
	if w.Runs != 2 {
		t.Errorf("runs = %d: the running one should still be counted", w.Runs)
	}
	if w.Avg < 9*time.Minute {
		t.Errorf("avg = %s: the unfinished run was averaged in as zero", w.Avg)
	}
}

// TestOnlyASucceedingAttemptsRowsAreCounted.
//
// Counting a failed attempt's rows reports work that was rolled back, and
// counting every attempt of a retried step reports the same rows twice --
// which is how a volume chart doubles on a bad night and looks like growth.
func TestOnlyASucceedingAttemptsRowsAreCounted(t *testing.T) {
	pool := insightsDB(t)
	id := finished(t, pool, "daily_sales", dom.StatusSuccess, time.Minute)
	withStages(t, pool, id, "load_failed", dom.StatusFailed, map[string]any{"rows": 999999})
	withStages(t, pool, id, "load", dom.StatusSuccess, map[string]any{"rows": 48213, "bytes": 1048576})

	w := find(t, window(t, pool), "daily_sales")
	if w.Rows != 48213 {
		t.Errorf("rows = %d, wanted 48213 — the failed attempt's rows were counted", w.Rows)
	}
	if w.Bytes != 1048576 {
		t.Errorf("bytes = %d", w.Bytes)
	}
}

// JSON has one number type, so a counter arrives as 48213 or as 48213.0
// depending on what serialised it. Postgres refuses '1234.0'::bigint outright,
// so the failure would be a report that stops being SENT -- a week of silence
// rather than a number that looks wrong.
//
// The JSON is written literally. The first version of this test passed a Go
// float64 and did not bite: encoding/json writes 1234.0 as `1234`, so the case
// it meant to cover never reached the database.
func TestACounterThatArrivedAsAFloatStillSums(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	id := finished(t, pool, "daily_sales", dom.StatusSuccess, time.Minute)

	const stages = `[{"indice":0,"nome":"load","estado":"done","numeros":{"rows":1234.0,"bytes":2048.0}}]`
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, etapas)
		VALUES ($1, $2, 'load', 'success', 0, $3)`,
		uuid.New(), id, stages); err != nil {
		t.Fatal(err)
	}

	w := find(t, window(t, pool), "daily_sales")
	if w.Rows != 1234 || w.Bytes != 2048 {
		t.Errorf("rows=%d bytes=%d, wanted 1234 and 2048", w.Rows, w.Bytes)
	}
}

// A step that is not an SDK one reports no numbers, which is most of them. The
// report has to come back with zero rather than fail.
func TestAWorkflowWithNoSDKStepsReportsNoVolume(t *testing.T) {
	pool := insightsDB(t)
	id := finished(t, pool, "shell_only", dom.StatusSuccess, time.Minute)
	withStages(t, pool, id, "echo", dom.StatusSuccess, nil)

	w := find(t, window(t, pool), "shell_only")
	if w.Rows != 0 || w.Bytes != 0 {
		t.Errorf("rows=%d bytes=%d for a workflow with no SDK step", w.Rows, w.Bytes)
	}
}

// An empty window says so, and the report says so too.
//
// A success rate of 100% out of nothing is the most reassuring number a report
// can print and the least true one -- an empty week usually means the scheduler
// was down, not that everything went well.
func TestAnEmptyWindowIsNotAHundredPercent(t *testing.T) {
	pool := insightsDB(t)
	rep := window(t, pool)
	if rep.Runs != 0 {
		t.Fatalf("runs = %d on an empty database", rep.Runs)
	}
	if rate := rep.SuccessRate(); rate >= 0 {
		t.Errorf("an empty window reported a success rate of %.1f%%", rate)
	}
}

// TestASkippedStepHasNotSucceeded.
//
// StepHasSucceeded decides whether the current run is a step's FIRST, which the
// SDK uses to create a destination table. A skipped step has never written
// anything, so counting it as a success would tell the next run "this has run
// before" and leave the table uncreated.
//
// The query already filters on success, so this asserts a property rather than
// changing one -- and it is the assertion that would catch somebody widening
// the filter to "terminal" one day.
func TestASkippedStepHasNotSucceeded(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	past := finished(t, pool, "daily_sales", dom.StatusSuccess, time.Minute)
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt)
		VALUES ($1, $2, 'load', $3, 0)`,
		uuid.New(), past, dom.StatusSkipped); err != nil {
		t.Fatal(err)
	}

	current := finished(t, pool, "daily_sales", dom.StatusRunning, 0)
	ran, err := repo.StepHasSucceeded(ctx, "daily_sales", "load", current)
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("a skipped step counted as an earlier success")
	}

	// And a real success does count, or the test above would pass with a
	// broken query.
	if _, err := pool.Exec(ctx, `UPDATE task_runs SET status = $2 WHERE run_id = $1`,
		past, dom.StatusSuccess); err != nil {
		t.Fatal(err)
	}
	ran, err = repo.StepHasSucceeded(ctx, "daily_sales", "load", current)
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("a real earlier success was not seen")
	}
}

// TestAlreadySucceededIsPerRunAndNotPerWorkflow.
//
// The two "has this succeeded" questions look identical and mean opposite
// things. StepHasSucceeded asks about EARLIER RUNS, and tells the SDK this is
// not a step's first time. AlreadySucceeded asks about THIS RUN, and tells the
// runner not to redo work on a retry.
//
// Confusing them makes a step skip itself forever after its first good day,
// which is the worst bug this pair could produce and the quietest.
func TestAlreadySucceededIsPerRunAndNotPerWorkflow(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	// Yesterday's run of the same workflow, where `load` succeeded.
	yesterday := finished(t, pool, "daily_sales", dom.StatusSuccess, time.Minute)
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt)
		VALUES ($1, $2, 'load', $3, 0)`, uuid.New(), yesterday, dom.StatusSuccess); err != nil {
		t.Fatal(err)
	}

	// Today's run, which has done nothing yet.
	today := finished(t, pool, "daily_sales", dom.StatusRunning, 0)

	settled, err := repo.AlreadySucceeded(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if settled[dom.Step("load")] {
		t.Error("yesterday's success made today's run skip the step; the two " +
			"questions have been confused")
	}
	// And the other question still answers yes, because it is about earlier
	// runs and that is what it is for.
	ever, err := repo.StepHasSucceeded(ctx, "daily_sales", "load", today)
	if err != nil {
		t.Fatal(err)
	}
	if !ever {
		t.Error("StepHasSucceeded stopped seeing an earlier run")
	}
}

// What a failed attempt of the same run left behind is not settled: the retry
// has to redo it, which is the whole point of retrying.
func TestAFailedStepOfTheSameRunIsNotSettled(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	id := finished(t, pool, "daily_sales", dom.StatusFailed, time.Minute)
	for node, status := range map[string]dom.Status{
		"extract": dom.StatusSuccess,
		"load":    dom.StatusFailed,
		"report":  dom.StatusSkipped,
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO task_runs (id, run_id, node_id, status, attempt)
			VALUES ($1, $2, $3, $4, 0)`, uuid.New(), id, node, status); err != nil {
			t.Fatal(err)
		}
	}

	settled, err := repo.AlreadySucceeded(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !settled[dom.Step("extract")] {
		t.Error("the step that worked is not settled, so the retry redoes it")
	}
	if settled[dom.Step("load")] {
		t.Error("a FAILED step is settled, so the retry would never redo it")
	}
	if settled[dom.Step("report")] {
		t.Error("a SKIPPED step is settled; it never ran, and the retry has to " +
			"decide about it again")
	}
}
