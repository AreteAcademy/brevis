package execution

import (
	"testing"

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

// TestEachInstanceAsksForItsOwnPod.
//
// The execution id becomes the POD's name. Four instances of a mapped step
// asking for four pods with one name is not four pods: the executor ADOPTS an
// existing pod rather than starting a second -- deliberately, so a process that
// dies midway does not leave two identical ones -- so three of the four would
// attach to the first one's logs and report its exit code as their own.
//
// The local executor ignores this field entirely, so the end-to-end tests
// cannot see the bug. This one can, and needs no cluster: the machine's
// contexts are real clusters.
func TestEachInstanceAsksForItsOwnPod(t *testing.T) {
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

// An UNMAPPED step's execution id is byte for byte what it was, so every pod
// name in every cluster running Brevis today stays the same.
func TestAnUnmappedStepsExecutionIdIsUnchanged(t *testing.T) {
	r := Runner{Processo: anyExecutor(t)}
	w := wf.Workflow{Slug: "pipeline", Nodes: []wf.Node{{ID: "load", Run: "echo hi"}}}
	_, task, err := r.build(w, w.Nodes[0], instance{key: run.Step("load")}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if task.ExecutionID != "pipeline:load" {
		t.Errorf("ExecutionID = %q, wanted the unchanged %q", task.ExecutionID, "pipeline:load")
	}
}
