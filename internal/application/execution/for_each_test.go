package execution_test

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// mapped runs extract -> load, where extract publishes `body` and load maps
// over `key`. Each instance appends "<index>:<value>" to a file, so the test
// reads exactly what each one was handed.
func mapped(t *testing.T, body, key string) ([]string, *spyPersister, error) {
	t.Helper()
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	seen := dir + "/seen"

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: `sh -c 'printf ` + "'\"'\"'" + body + "'\"'\"'" + ` > "$BREVIS_OUTPUT"'`},
			{ID: "load", ForEach: key,
				Run: `sh -c 'echo "$BREVIS_MAP_INDEX:$BREVIS_MAP_VALUE" >> ` + seen + `'`},
		},
		Edges: []wf.Edge{{From: "extract", To: "load"}},
	}
	spy := &spyPersister{}
	runErr := app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: spy, RunID: uuid.New(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)

	raw, err := os.ReadFile(seen)
	if err != nil {
		return nil, spy, runErr
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	sort.Strings(lines)
	return lines, spy, runErr
}

// TestAMappedStepRunsOncePerElement, and each instance is handed its own.
func TestAMappedStepRunsOncePerElement(t *testing.T) {
	lines, spy, err := mapped(t, `{"partitions":["2026-01","2026-02","2026-03"]}`, "extract.partitions")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0:2026-01", "1:2026-02", "2:2026-03"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Errorf("instances saw %v, wanted %v", lines, want)
	}

	// One ROW per instance. Without map_index they would collide on
	// (run, node, attempt) and the last to finish would overwrite the rest --
	// one table saying three things happened and remembering one.
	rows := spy.instances()
	for i := range 3 {
		if _, ok := rows[dom.StepKey{Node: "load", MapIndex: i}]; !ok {
			t.Errorf("no row for instance %d: %v", i, rows)
		}
	}
	// And extract keeps its unmapped key, so nothing else moved.
	if _, ok := rows[dom.Step("extract")]; !ok {
		t.Errorf("the unmapped step lost its row: %v", rows)
	}
}

// A JSON string arrives WITHOUT its quotes: `for_each` over ["2026-01"] hands a
// shell 2026-01, which is what the command was written expecting. Anything else
// arrives as its JSON.
func TestTheElementArrivesInTheShapeTheCommandExpects(t *testing.T) {
	lines, _, err := mapped(t, `{"items":["a",7,{"b":1}]}`, "extract.items")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0:a", `1:7`, `2:{"b":1}`}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Errorf("instances saw %v, wanted %v", lines, want)
	}
}

// TestAnEmptyListIsSkippedAndNotSuccess.
//
// A step that did nothing because there was nothing to do did not succeed at
// doing it. A green node over zero instances is exactly the kind of thing
// somebody builds a dashboard on.
func TestAnEmptyListIsSkippedAndNotSuccess(t *testing.T) {
	_, spy, err := mapped(t, `{"partitions":[]}`, "extract.partitions")
	if err != nil {
		t.Fatalf("an empty list failed the run: %v", err)
	}
	reason, skipped := spy.skips()["load"]
	if !skipped {
		t.Fatalf("an empty list did not skip the step: %v", spy.instances())
	}
	if !strings.Contains(reason, "empty list") {
		t.Errorf("the reason does not say why: %q", reason)
	}
}

// A value that is not a list is a failure, not a list of one. Guessing would
// make `for_each: x` behave differently depending on how many elements
// happened to be there today.
func TestAScalarIsNotAListOfOne(t *testing.T) {
	_, _, err := mapped(t, `{"partitions":"2026-01"}`, "extract.partitions")
	if err == nil {
		t.Fatal("a scalar was mapped over as though it were a list")
	}
	if !strings.Contains(err.Error(), "not a list") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
}

// The same policy `unless_empty:` has, and for the same reason: a typo that
// silently produced zero instances would disable the step forever with nothing
// saying why.
func TestAMissingListFailsAndNamesWhatWasPublished(t *testing.T) {
	_, _, err := mapped(t, `{"partitions":["a"]}`, "extract.partition")
	if err == nil {
		t.Fatal("a typo in the key produced no error")
	}
	if !strings.Contains(err.Error(), `"partitions"`) {
		t.Errorf("the error does not name what was published: %v", err)
	}
}

// A step with no `for_each:` is one instance with the unmapped key, and its
// environment carries neither variable. A variable that is always there and
// always empty teaches whoever reads the environment to ignore it.
func TestAnUnmappedStepIsUntouched(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	seen := dir + "/seen"
	w := wf.Workflow{
		Slug:  "pipeline",
		Nodes: []wf.Node{{ID: "solo", Run: `sh -c 'echo "[$BREVIS_MAP_INDEX][$BREVIS_MAP_VALUE]" > ` + seen + `'`}},
	}
	spy := &spyPersister{}
	if err := (app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: spy, RunID: uuid.New(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "[][]" {
		t.Errorf("an unmapped step got mapping variables: %q", raw)
	}
	if _, ok := spy.instances()[dom.Step("solo")]; !ok {
		t.Errorf("the unmapped key was not used: %v", spy.instances())
	}
}

// TestOneFailedInstanceFailsTheStep.
//
// Three partitions loading and one not is a step that did not do its job, and
// the steps below it have to see that. A node that goes green because most of
// it worked is the badge that lies.
func TestOneFailedInstanceFailsTheStep(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: `sh -c 'printf ` + "'\"'\"'" + `{"p":["ok","bad","ok"]}` + "'\"'\"'" + ` > "$BREVIS_OUTPUT"'`},
			{ID: "load", ForEach: "extract.p",
				Run: `sh -c 'test "$BREVIS_MAP_VALUE" != bad'`},
			{ID: "report", Run: `sh -c 'true'`},
		},
		Edges: []wf.Edge{{From: "extract", To: "load"}, {From: "load", To: "report"}},
	}
	spy := &spyPersister{}
	err = app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: spy, RunID: uuid.New(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)
	if err == nil {
		t.Fatal("one failed instance did not fail the step")
	}
	// And the step below saw it.
	if _, skipped := spy.skips()["report"]; !skipped {
		t.Errorf("the step below a partly-failed mapped step ran: %v", spy.skips())
	}
	// The instances that worked kept their own success.
	rows := spy.instances()
	if rows[dom.StepKey{Node: "load", MapIndex: 0}] != dom.StatusSuccess {
		t.Errorf("an instance that worked was not recorded as success: %v", rows)
	}
	if rows[dom.StepKey{Node: "load", MapIndex: 1}] != dom.StatusFailed {
		t.Errorf("the instance that broke was not recorded as failed: %v", rows)
	}
}

// TestIntegrationARetryRedoesOnlyTheInstancesThatFailed.
//
// The interaction between mapping and resume, and the reason AlreadySucceeded
// is keyed by instance rather than by node: twenty partitions do not fail
// together, and a retry that redid all twenty because two broke would be the
// old behaviour wearing a new column.
func TestIntegrationARetryRedoesOnlyTheInstancesThatFailed(t *testing.T) {
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
	ran := dir + "/ran"
	flag := dir + "/let-bad-pass"

	w := wf.Workflow{
		Slug: "map_retry", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: `sh -c 'printf ` + "'\"'\"'" + `{"p":["ok1","bad","ok2"]}` + "'\"'\"'" + ` > "$BREVIS_OUTPUT"'`},
			{ID: "load", ForEach: "extract.p", Run: `sh -c '` +
				`echo "$BREVIS_MAP_VALUE" >> ` + ran + `; ` +
				`test "$BREVIS_MAP_VALUE" != bad || test -f ` + flag + `'`},
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

	// The retry: only the instance that broke runs again.
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, w); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}

	raw, err := os.ReadFile(ran)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	counted := map[string]int{}
	for _, l := range lines {
		counted[l]++
	}
	if counted["bad"] != 2 {
		t.Errorf("the failing instance ran %d times, wanted 2", counted["bad"])
	}
	for _, ok := range []string{"ok1", "ok2"} {
		if counted[ok] != 1 {
			t.Errorf("%s ran %d times; a settled instance was redone", ok, counted[ok])
		}
	}

	// And the card counts four of four, from the rows.
	states, err := repo.NodeStates(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	load := states["load"]
	if load.Instances != 3 || load.Done != 3 {
		t.Errorf("the card would show [%d/%d], wanted [3]", load.Done, load.Instances)
	}
	if load.Status != "success" {
		t.Errorf("the mapped step reads %q after every instance worked", load.Status)
	}
	// The unmapped step carries no counts, so its payload is what it was.
	if states["extract"].Instances != 0 {
		t.Errorf("an unmapped step got a count: %d", states["extract"].Instances)
	}
}
