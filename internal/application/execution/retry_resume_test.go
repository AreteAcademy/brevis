package execution_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/local"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// retryOf runs a workflow as though an earlier attempt of the SAME run had
// already finished `settled`.
func retryOf(t *testing.T, w wf.Workflow, settled []string, failing ...string) (*ran, *spyPersister, error) {
	t.Helper()
	reg := execution.NewRegistry()
	done := &ran{}
	broken := map[string]bool{}
	for _, f := range failing {
		broken[f] = true
	}
	for _, n := range w.Nodes {
		if n.Marker {
			continue
		}
		id := n.ID
		reg.MustRegister(execution.FuncTask{TaskName: id, Fn: func(context.Context, execution.Input) error {
			done.add(id)
			if broken[id] {
				return errors.New("boom")
			}
			return nil
		}})
	}
	for i := range w.Nodes {
		if !w.Nodes[i].Marker {
			w.Nodes[i].Action = w.Nodes[i].ID
		}
	}

	already := map[dom.StepKey]bool{}
	for _, s := range settled {
		already[dom.Step(s)] = true
	}

	spy := &spyPersister{}
	err := app.Runner{
		Go: local.NewGoExecutor(reg), Persist: spy, RunID: uuid.New(),
		History: &historico{settled: already},
	}.Run(context.Background(), w)
	return done, spy, err
}

// A chain: expensive -> flaky -> report.
func chain() wf.Workflow {
	return wf.Workflow{
		Slug:  "w",
		Nodes: []wf.Node{{ID: "expensive"}, {ID: "flaky"}, {ID: "report"}},
		Edges: []wf.Edge{{From: "expensive", To: "flaky"}, {From: "flaky", To: "report"}},
	}
}

// TestARetryDoesNotReRunWhatAlreadySucceeded is the change itself.
//
// A retry used to re-run the whole graph: a workflow with an expensive step
// beside a flaky one paid for the expensive one on every attempt, and any step
// that was not idempotent did its work twice.
func TestARetryDoesNotReRunWhatAlreadySucceeded(t *testing.T) {
	done, _, err := retryOf(t, chain(), []string{"expensive"})
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if done.has("expensive") {
		t.Error("a step that already succeeded ran again")
	}
	// And what had not finished does run -- the retry has to be a retry.
	for _, want := range []string{"flaky", "report"} {
		if !done.has(want) {
			t.Errorf("%s did not run on the retry: %v", want, done.list())
		}
	}
}

// TestAStepThatAlreadySucceededIsNotMarkedSkipped.
//
// It is a success, its row says so, and overwriting that row with a skip would
// erase the only record of the work actually happening -- including its
// duration, its log and what it published.
func TestAStepThatAlreadySucceededIsNotMarkedSkipped(t *testing.T) {
	_, spy, err := retryOf(t, chain(), []string{"expensive"})
	if err != nil {
		t.Fatal(err)
	}
	if reason, marked := spy.skips()["expensive"]; marked {
		t.Errorf("a finished step was overwritten as skipped: %q", reason)
	}
}

// A step that already succeeded counts as a SUCCESS for the steps below it, or
// every one of them would be skipped on the retry with "`expensive` was
// pending" -- which is the failure mode this could most easily have.
func TestTheStepsBelowSeeItAsSucceeded(t *testing.T) {
	done, spy, err := retryOf(t, chain(), []string{"expensive", "flaky"})
	if err != nil {
		t.Fatal(err)
	}
	if !done.has("report") {
		t.Errorf("report was held back by steps that had already finished: %v", spy.skips())
	}
	if len(spy.skips()) != 0 {
		t.Errorf("something was skipped on a retry where everything was fine: %v", spy.skips())
	}
}

// A retry where the same step fails again still fails, and still stops the
// branch below it. Not re-running what worked must not soften what did not.
func TestARetryThatFailsAgainStillFails(t *testing.T) {
	done, spy, err := retryOf(t, chain(), []string{"expensive"}, "flaky")
	if err == nil {
		t.Fatal("the retry succeeded with a step still failing")
	}
	if !done.has("flaky") {
		t.Error("the failing step was not retried")
	}
	if done.has("report") {
		t.Error("the step below the failure ran")
	}
	if _, recorded := spy.skips()["report"]; !recorded {
		t.Error("report was not recorded as skipped")
	}
}

// TestTheFirstAttemptSettlesNothing: a run's first attempt has no history, and
// every step runs. It is the case every workflow hits, and the one that would
// break if the two "has this succeeded" questions were ever confused.
func TestTheFirstAttemptSettlesNothing(t *testing.T) {
	done, _, err := retryOf(t, chain(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"expensive", "flaky", "report"} {
		if !done.has(want) {
			t.Errorf("%s did not run on a first attempt: %v", want, done.list())
		}
	}
}

// With no History at all -- `brevis run` on a laptop -- everything runs. There
// is no earlier attempt to resume from, and inventing one would be worse than
// re-running.
func TestWithNoHistoryEverythingRuns(t *testing.T) {
	reg := execution.NewRegistry()
	done := &ran{}
	for _, id := range []string{"a", "b"} {
		name := id
		reg.MustRegister(execution.FuncTask{TaskName: name, Fn: func(context.Context, execution.Input) error {
			done.add(name)
			return nil
		}})
	}
	w := wf.Workflow{
		Slug:  "w",
		Nodes: []wf.Node{{ID: "a", Action: "a"}, {ID: "b", Action: "b"}},
		Edges: []wf.Edge{{From: "a", To: "b"}},
	}
	if err := (app.Runner{Go: local.NewGoExecutor(reg)}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if len(done.list()) != 2 {
		t.Errorf("ran %v, wanted both", done.list())
	}
}

// TestIntegrationARetryReRunsOnlyWhatFailed is the same claim as the tests
// above, through the real query and the real runner, against Postgres.
//
// The unit tests hand the runner a map. This one makes an attempt actually
// fail, writes what really happened, and runs the SAME run id again -- which is
// the only way to find out that AlreadySucceeded asks the question the runner
// meant to ask.
func TestIntegrationARetryReRunsOnlyWhatFailed(t *testing.T) {
	dsn := os.Getenv("BREVIS_IT_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_IT_PG_DSN ausente")
	}
	ctx := context.Background()
	pool, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	runID := uuid.New()
	if err := createRun(ctx, pool, runID); err != nil {
		t.Fatalf("criando a run: %v", err)
	}
	repo := postgres.NewRunRepo(pool)

	var expensive, flaky int
	fails := true
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{TaskName: "expensive",
		Fn: func(context.Context, execution.Input) error { expensive++; return nil }})
	reg.MustRegister(execution.FuncTask{TaskName: "flaky", Fn: func(context.Context, execution.Input) error {
		flaky++
		if fails {
			return errors.New("the vendor was down")
		}
		return nil
	}})

	w := wf.Workflow{
		Slug: "retry_e2e",
		Nodes: []wf.Node{
			{ID: "expensive", Action: "expensive"},
			{ID: "flaky", Action: "flaky"},
			{ID: "report", Action: "expensive"},
		},
		Edges: []wf.Edge{{From: "expensive", To: "flaky"}, {From: "flaky", To: "report"}},
	}
	runner := app.Runner{
		RunID: runID, Persist: repo, History: repo,
		Go: local.NewGoExecutor(reg),
	}

	// Attempt one: expensive works, flaky does not, report is skipped.
	if err := runner.Run(ctx, w); err == nil {
		t.Fatal("the first attempt should have failed")
	}
	if expensive != 1 || flaky != 1 {
		t.Fatalf("first attempt: expensive=%d flaky=%d", expensive, flaky)
	}

	// The precondition, asserted rather than assumed. Without it, anything that
	// clears the table between the two attempts fails the test below with
	// "expensive ran twice", which blames the feature for somebody else's
	// TRUNCATE.
	settled, err := repo.AlreadySucceeded(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !settled[dom.Step("expensive")] {
		t.Fatalf("the first attempt's success is not in the table: %v", settled)
	}

	// Attempt two, same run id, the way the dispatcher retries.
	fails = false
	if err := runner.Run(ctx, w); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if expensive != 2 {
		// One for `expensive` on the first attempt, one for `report` -- which
		// shares the task but is a different node and had never run.
		t.Errorf("expensive ran %d times; the settled step was re-run", expensive)
	}
	if flaky != 2 {
		t.Errorf("flaky ran %d times; the failed step was not retried", flaky)
	}

	// And the database agrees: the step that worked keeps its original row
	// rather than being overwritten as skipped.
	states, err := repo.NodeStates(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for node, want := range map[string]string{
		"expensive": "success", "flaky": "success", "report": "success",
	} {
		if got := states[node].Status; got != want {
			t.Errorf("%s = %s, wanted %s", node, got, want)
		}
	}
}

// TestIntegrationTheRetryStillSeesWhatTheSettledStepPublished.
//
// The interaction most likely to break, and the quietest if it does: a step
// that is not re-run cannot publish anything on this attempt, so the step below
// it would find nothing where the first attempt left something.
//
// It works because seedContext already reads the run's published context back
// out of the database -- built for the resumed run before any step was
// skipped. This asserts the two features agree, which nothing else does.
func TestIntegrationTheRetryStillSeesWhatTheSettledStepPublished(t *testing.T) {
	dsn := os.Getenv("BREVIS_IT_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_IT_PG_DSN ausente")
	}
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	runID := uuid.New()
	if err := createRun(ctx, pool, runID); err != nil {
		t.Fatalf("criando a run: %v", err)
	}
	repo := postgres.NewRunRepo(pool)

	dir := t.TempDir()
	flag := filepath.Join(dir, "let-it-pass")
	seen := filepath.Join(dir, "seen")

	w := wf.Workflow{
		Slug: "resume_ctx", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: `sh -c 'printf "{\"bucket\":\"s3://landing\"}" > "$BREVIS_OUTPUT"'`},
			// Fails until the flag file exists, so the first attempt stops here.
			{ID: "load", Run: `sh -c 'test -f ` + flag + ` || exit 3; echo "$BREVIS_INPUT" > ` + seen + `'`},
		},
		Edges: []wf.Edge{{From: "extract", To: "load"}},
	}
	r := app.Runner{
		RunID: runID, Persist: repo, History: repo,
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}

	if err := r.Run(ctx, w); err == nil {
		t.Fatal("the first attempt should have failed")
	}

	// The retry: extract is settled and does NOT run, so nothing writes the
	// bucket again on this attempt.
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, w); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}

	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the step below never ran: %v", err)
	}
	if !strings.Contains(string(raw), "s3://landing") {
		t.Errorf("the retry lost what the settled step published: %s", raw)
	}
}
