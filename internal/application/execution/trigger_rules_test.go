package execution_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// ran records which tasks actually executed, which is the only thing these
// tests are about.
type ran struct {
	mu sync.Mutex
	by []string
}

func (r *ran) add(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by = append(r.by, name)
}

func (r *ran) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.by...)
	sort.Strings(out)
	return out
}

func (r *ran) has(name string) bool {
	for _, n := range r.list() {
		if n == name {
			return true
		}
	}
	return false
}

// flow builds a registry where the named tasks fail and everything else passes,
// runs the workflow, and reports what ran and what was recorded as skipped.
func flow(t *testing.T, w wf.Workflow, failing ...string) (*ran, *spyPersister, error) {
	t.Helper()
	reg := execution.NewRegistry()
	done := &ran{}
	broken := map[string]bool{}
	for _, f := range failing {
		broken[f] = true
	}
	for _, n := range w.Nodes {
		if n.Marker {
			// A marker declares no work, and Validate refuses one that does.
			// Registering a task for it would make this helper build a
			// workflow the engine would never accept.
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

	spy := &spyPersister{}
	err := app.Runner{
		Go: local.NewGoExecutor(reg), Persist: spy, RunID: uuid.New(),
	}.Run(context.Background(), w)
	return done, spy, err
}

// extract -> (notify_failure | transform), and transform -> report.
func branching(notifyWhen string) wf.Workflow {
	return wf.Workflow{
		Slug: "w",
		Nodes: []wf.Node{
			{ID: "extract"},
			{ID: "notify_failure", When: notifyWhen},
			{ID: "transform"},
			{ID: "report"},
		},
		Edges: []wf.Edge{
			{From: "extract", To: "notify_failure"},
			{From: "extract", To: "transform"},
			{From: "transform", To: "report"},
		},
	}
}

// TestAnyFailedFiresWhenSomethingFailed. Half of the assertion; the other half
// is the test below, and neither is worth much without it.
func TestAnyFailedFiresWhenSomethingFailed(t *testing.T) {
	done, spy, err := flow(t, branching(wf.WhenAnyFailed), "extract")
	if err == nil {
		t.Fatal("the run should have failed")
	}
	if !done.has("notify_failure") {
		t.Errorf("the handler did not run: %v", done.list())
	}
	// And everything under the default rule did not.
	for _, quiet := range []string{"transform", "report"} {
		if done.has(quiet) {
			t.Errorf("%s ran after a failure", quiet)
		}
		if _, recorded := spy.skips()[quiet]; !recorded {
			t.Errorf("%s was not recorded as skipped, so the screen shows it pending forever", quiet)
		}
	}
}

// TestAnyFailedDoesNotFireWhenNothingFailed is the assertion that bites. A rule
// that always fires passes the previous test.
func TestAnyFailedDoesNotFireWhenNothingFailed(t *testing.T) {
	done, spy, err := flow(t, branching(wf.WhenAnyFailed))
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if done.has("notify_failure") {
		t.Error("the failure handler ran on a run where nothing failed")
	}
	if reason := spy.skips()["notify_failure"]; !strings.Contains(reason, "failed") {
		t.Errorf("the reason does not explain itself: %q", reason)
	}
	// Everything else did run: a handler that is not needed must not hold the
	// rest of the graph back.
	for _, want := range []string{"extract", "transform", "report"} {
		if !done.has(want) {
			t.Errorf("%s did not run: %v", want, done.list())
		}
	}
}

// TestAllDoneRunsEitherWay is the case that is impossible today: a cleanup that
// has to happen whether or not the thing before it worked.
func TestAllDoneRunsEitherWay(t *testing.T) {
	w := wf.Workflow{
		Slug: "w",
		Nodes: []wf.Node{
			{ID: "extract"}, {ID: "transform"}, {ID: "cleanup", When: wf.WhenAllDone},
		},
		Edges: []wf.Edge{
			{From: "extract", To: "transform"},
			{From: "extract", To: "cleanup"}, {From: "transform", To: "cleanup"},
		},
	}

	for _, c := range []struct {
		name    string
		failing []string
	}{
		{"everything worked", nil},
		{"the first step failed", []string{"extract"}},
		{"the middle step failed", []string{"transform"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			done, _, _ := flow(t, w, c.failing...)
			if !done.has("cleanup") {
				t.Errorf("cleanup did not run (%v)", done.list())
			}
		})
	}
}

// TestARunStillFailsWhenAHandlerSucceeds.
//
// A trigger rule decides which steps RUN. It does not decide the run's
// outcome -- a notify_failure that delivered its message does not mean the
// pipeline worked, and a green run with a failed step in it is exactly the
// "partial result that looks complete" this engine refuses.
func TestARunStillFailsWhenAHandlerSucceeds(t *testing.T) {
	_, _, err := flow(t, branching(wf.WhenAnyFailed), "extract")
	if err == nil {
		t.Fatal("the run succeeded with a failed step in it")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("the error lost the cause: %v", err)
	}
}

// TestAnUnrelatedBranchKeepsGoing is Airflow's rule, and the reason this engine
// changed to it: `all_success` asks about a step's OWN dependencies and nothing
// else.
//
// This engine used to abort the whole graph at the first failure. That had a
// reason -- a partial result that looked complete, a pipeline 28 days late --
// and what replaces it is that the run still fails, the skipped steps say why,
// and the alert still goes out. What is given up is asserted here: an unrelated
// branch now writes its data on a run that failed elsewhere.
func TestAnUnrelatedBranchKeepsGoing(t *testing.T) {
	w := wf.Workflow{
		Slug: "w",
		Nodes: []wf.Node{
			{ID: "broken"}, {ID: "healthy"}, {ID: "after_healthy"},
		},
		// after_healthy depends only on healthy. Nothing connects it to broken.
		Edges: []wf.Edge{{From: "healthy", To: "after_healthy"}},
	}
	done, spy, err := flow(t, w, "broken")

	if !done.has("after_healthy") {
		t.Error("a branch that does not touch the failure was stopped by it")
	}
	if _, skipped := spy.skips()["after_healthy"]; skipped {
		t.Error("a healthy branch was recorded as skipped")
	}

	// And the run still FAILS. A trigger rule decides which steps run, never
	// what the run ended as -- a green run with a failed step in it is the
	// "partial result that looks complete" this engine refuses, and it is what
	// the abort used to protect against.
	if err == nil {
		t.Fatal("the run succeeded with a failed step in it")
	}
}

// A branch BELOW the failure still stops, which is the half of the old
// behaviour that stays. `all_success` being local does not mean it is absent.
func TestTheBranchBelowAFailureStillStops(t *testing.T) {
	w := wf.Workflow{
		Slug:  "w",
		Nodes: []wf.Node{{ID: "extract"}, {ID: "transform"}, {ID: "report"}},
		Edges: []wf.Edge{{From: "extract", To: "transform"}, {From: "transform", To: "report"}},
	}
	done, spy, err := flow(t, w, "extract")
	if err == nil {
		t.Fatal("the run should have failed")
	}
	for _, quiet := range []string{"transform", "report"} {
		if done.has(quiet) {
			t.Errorf("%s ran below a failed step", quiet)
		}
		if _, recorded := spy.skips()[quiet]; !recorded {
			t.Errorf("%s was not recorded as skipped", quiet)
		}
	}
	// The skip propagates for the right reason: transform was stopped by
	// extract, and report by transform. Not by "the run failed".
	if reason := spy.skips()["report"]; !strings.Contains(reason, "transform") {
		t.Errorf("report's reason does not name transform: %q", reason)
	}
}

// A skip is recorded with a reason naming the step responsible. "Skipped" alone
// sends whoever is looking at the graph to trace edges by hand.
func TestASkipSaysWhichStepCausedIt(t *testing.T) {
	w := wf.Workflow{
		Slug:  "w",
		Nodes: []wf.Node{{ID: "extract"}, {ID: "transform"}, {ID: "report"}},
		Edges: []wf.Edge{{From: "extract", To: "transform"}, {From: "transform", To: "report"}},
	}
	_, spy, _ := flow(t, w, "extract")

	if reason := spy.skips()["transform"]; !strings.Contains(reason, "extract") {
		t.Errorf("transform's reason does not name extract: %q", reason)
	}
}

// `when: any_failed` on a step with no dependencies can never be satisfied:
// there is nothing it waits for. It is skipped saying so, rather than running
// on every single run, which is what a rule read as "the default" would do.
func TestAnyFailedWithNoDependenciesIsSkippedAndSaysWhy(t *testing.T) {
	w := wf.Workflow{
		Slug:  "w",
		Nodes: []wf.Node{{ID: "lonely", When: wf.WhenAnyFailed}},
	}
	done, spy, err := flow(t, w)
	if err != nil {
		t.Fatal(err)
	}
	if done.has("lonely") {
		t.Error("a failure handler with nothing to watch ran anyway")
	}
	if reason := spy.skips()["lonely"]; !strings.Contains(reason, "depends_on") {
		t.Errorf("the reason does not point at the missing depends_on: %q", reason)
	}
}

// A run with no persister still applies the rules. `brevis run` on a laptop has
// none, and a laptop must not get different execution semantics from a cluster.
func TestTheRulesApplyWithNoPersister(t *testing.T) {
	reg := execution.NewRegistry()
	done := &ran{}
	for _, id := range []string{"extract", "notify"} {
		name := id
		reg.MustRegister(execution.FuncTask{TaskName: name, Fn: func(context.Context, execution.Input) error {
			done.add(name)
			if name == "extract" {
				return errors.New("boom")
			}
			return nil
		}})
	}
	w := wf.Workflow{
		Slug: "w",
		Nodes: []wf.Node{
			{ID: "extract", Action: "extract"},
			{ID: "notify", Action: "notify", When: wf.WhenAnyFailed},
		},
		Edges: []wf.Edge{{From: "extract", To: "notify"}},
	}
	if err := (app.Runner{Go: local.NewGoExecutor(reg)}).Run(context.Background(), w); err == nil {
		t.Fatal("the run should have failed")
	}
	if !done.has("notify") {
		t.Error("the handler did not run without a persister")
	}
}

// A marker runs, succeeds and appears on the screen -- without an executor,
// without a pod, and without an image pull to accomplish nothing.
func TestAMarkerRunsWithoutAnExecutor(t *testing.T) {
	reg := execution.NewRegistry()
	done := &ran{}
	reg.MustRegister(execution.FuncTask{TaskName: "work", Fn: func(context.Context, execution.Input) error {
		done.add("work")
		return nil
	}})

	w := wf.Workflow{
		Slug: "w",
		Nodes: []wf.Node{
			{ID: "start", Marker: true},
			{ID: "work", Action: "work"},
			{ID: "end", Marker: true},
		},
		Edges: []wf.Edge{{From: "start", To: "work"}, {From: "work", To: "end"}},
	}
	spy := &spyPersister{}
	// No Processo and no Pods: a marker that needed either would fail here
	// with "no process executor configured", which is exactly the point.
	err := app.Runner{
		Go: local.NewGoExecutor(reg), Persist: spy, RunID: uuid.New(),
	}.Run(context.Background(), w)
	if err != nil {
		t.Fatalf("a workflow of two markers and one step failed: %v", err)
	}
	if !done.has("work") {
		t.Error("the real step did not run")
	}
	if len(spy.skips()) != 0 {
		t.Errorf("something was skipped: %v", spy.skips())
	}
}

// TestAnEndMarkerWaitsForEverything.
//
// It is what the step is for: an `end` that depends on every branch turns "did
// the whole thing finish?" into one node instead of six arrows to follow. So it
// must NOT run when a branch failed -- a green `end` under a red branch is the
// worst possible version of this feature.
func TestAnEndMarkerDoesNotRunWhenABranchFailed(t *testing.T) {
	w := wf.Workflow{
		Slug: "w",
		Nodes: []wf.Node{
			{ID: "left"}, {ID: "right"}, {ID: "end", Marker: true},
		},
		Edges: []wf.Edge{{From: "left", To: "end"}, {From: "right", To: "end"}},
	}
	done, spy, err := flow(t, w, "right")
	if err == nil {
		t.Fatal("the run should have failed")
	}
	if done.has("end") {
		t.Error("the end marker ran under a failed branch")
	}
	if _, recorded := spy.skips()["end"]; !recorded {
		t.Error("the end marker was not recorded as skipped")
	}
}
