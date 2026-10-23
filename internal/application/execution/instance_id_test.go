package execution

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	run "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// anyExecutor exists only so build() gets past its "no process executor"
// refusal. Nothing here runs anything.
func anyExecutor(t *testing.T) *local.ProcessExecutor {
	t.Helper()
	e, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestEachInstanceGetsItsOwnExecutionId.
//
// Each instance of a mapped step is its own execution, with its own id. This
// test said the id BECOMES the pod's name; it does not -- PodName builds the
// name from the task's fields, and the instance reached it only with #127
// (see TaskExec.MapIndex and PodName's own tests). What this one holds is the
// agent's half: one id per instance, so the agent never refuses the second as
// "already running here".
func TestEachInstanceGetsItsOwnExecutionId(t *testing.T) {
	r := Runner{Processo: anyExecutor(t)}
	w := wf.Workflow{Slug: "pipeline", Nodes: []wf.Node{{ID: "load", Run: "echo hi"}}}
	n := w.Nodes[0]

	seen := map[string]bool{}
	for i := range 3 {
		_, task, err := r.build(w, n, instance{key: run.StepKey{Node: "load", MapIndex: i}}, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if seen[task.ExecutionID] {
			t.Fatalf("instance %d reuses the execution id %q", i, task.ExecutionID)
		}
		seen[task.ExecutionID] = true
	}
}

// #127: the execution id named a STEP -- "pipeline:load" -- and the agent keys
// its ring, its process and its state file on it, refusing a second start of
// one. Two runs of one workflow on one agent collided: the second failed at
// once. The id now names one execution, and reads as its identity.
//
// It replaces a test that pinned "pipeline:load" so pod names would not move.
// Pod names never came from this id; that property is PodName's, and its own
// tests hold it.
func TestAnExecutionIdReadsAsItsIdentity(t *testing.T) {
	runID := uuid.MustParse("6f1d2c3b-0000-4000-8000-000000000127")
	r := Runner{Processo: anyExecutor(t), RunID: runID, RunAttempt: 1}
	w := wf.Workflow{Slug: "pipeline", Nodes: []wf.Node{{ID: "load", Run: "echo hi"}}}

	_, task, err := r.build(w, w.Nodes[0], instance{key: run.Step("load")}, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pipeline:load:" + runID.String() + ":r1:a2"; task.ExecutionID != want {
		t.Errorf("ExecutionID = %q, wanted %q", task.ExecutionID, want)
	}
}

// Every part of the identity changes the id: another run, another attempt of
// the run, another attempt of the step. "A retry gets a new id" is the agent's
// contract, and this is the side that keeps it.
func TestNoTwoExecutionsShareAnId(t *testing.T) {
	w := wf.Workflow{Slug: "pipeline", Nodes: []wf.Node{{ID: "load", Run: "echo hi"}}}
	id := func(runID uuid.UUID, runAttempt, attempt int) string {
		t.Helper()
		r := Runner{Processo: anyExecutor(t), RunID: runID, RunAttempt: runAttempt}
		_, task, err := r.build(w, w.Nodes[0], instance{key: run.Step("load")}, attempt, false)
		if err != nil {
			t.Fatal(err)
		}
		return task.ExecutionID
	}
	a, b := uuid.New(), uuid.New()
	seen := map[string]string{}
	for name, got := range map[string]string{
		"run a, 0, 0": id(a, 0, 0),
		"run b, 0, 0": id(b, 0, 0),
		"run a, 1, 0": id(a, 1, 0),
		"run a, 0, 1": id(a, 0, 1),
	} {
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s share the execution id %q", name, other, got)
		}
		seen[got] = name
	}
}

// The task carries the run's attempt and its instance. RunAttempt existed so a
// retried run would not adopt the previous attempt's pods, and PodName reads
// it -- but build never set it, so it was always 0 (#127).
func TestTheTaskCarriesTheRunsAttemptAndItsInstance(t *testing.T) {
	r := Runner{Processo: anyExecutor(t), RunID: uuid.New(), RunAttempt: 2}
	w := wf.Workflow{Slug: "pipeline", Nodes: []wf.Node{{ID: "load", Run: "echo hi"}}}

	_, mapped, err := r.build(w, w.Nodes[0], instance{key: run.StepKey{Node: "load", MapIndex: 3}}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.RunAttempt != 2 || mapped.MapIndex != 3 {
		t.Errorf("RunAttempt = %d, MapIndex = %d; wanted 2 and 3", mapped.RunAttempt, mapped.MapIndex)
	}
	_, plain, err := r.build(w, w.Nodes[0], instance{key: run.Step("load")}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if plain.MapIndex != run.Unmapped {
		t.Errorf("an unmapped step's MapIndex = %d, wanted %d", plain.MapIndex, run.Unmapped)
	}
}

// The id crosses the wire as a path segment -- POST /v1/exec/{id}/resume, sent
// unescaped -- and becomes the agent's state file name. Whatever it is made of
// has to come back out of the agent's own router unchanged.
func TestAnExecutionIdSurvivesTheAgentsRoute(t *testing.T) {
	r := Runner{Processo: anyExecutor(t), RunID: uuid.New(), RunAttempt: 1}
	w := wf.Workflow{Slug: "daily_sales", Nodes: []wf.Node{{ID: "load_orders", Run: "echo hi"}}}
	_, task, err := r.build(w, w.Nodes[0], instance{key: run.StepKey{Node: "load_orders", MapIndex: 12}}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task.ExecutionID, "/") {
		t.Fatalf("the id %q holds a slash: it would split the route and the file name", task.ExecutionID)
	}

	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/exec/{id}/resume", func(_ http.ResponseWriter, req *http.Request) {
		got = req.PathValue("id")
	})
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/exec/"+task.ExecutionID+"/resume", nil))
	if got != task.ExecutionID {
		t.Errorf("the agent's route read %q out of %q", got, task.ExecutionID)
	}
}
