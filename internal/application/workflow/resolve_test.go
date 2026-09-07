package workflow_test

import (
	"sort"
	"strings"
	"testing"

	spec "github.com/AreteAcademy/brevis/internal/application/workflow"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// resolve parses a set of files and expands `uses:` across it, the way both
// `validate` and `publish` do.
func resolve(t *testing.T, files ...string) ([]wf.Workflow, error) {
	t.Helper()
	var all []wf.Workflow
	for i, body := range files {
		w, err := spec.Parse("f"+string(rune('a'+i))+".yaml", []byte(body))
		if err != nil {
			t.Fatalf("parsing file %d: %v", i, err)
		}
		all = append(all, w)
	}
	return spec.Resolve(all)
}

func find(t *testing.T, all []wf.Workflow, slug string) wf.Workflow {
	t.Helper()
	for _, w := range all {
		if w.Slug == slug {
			return w
		}
	}
	t.Fatalf("%q is not in %d workflows", slug, len(all))
	return wf.Workflow{}
}

func ids(w wf.Workflow) []string {
	out := make([]string, 0, len(w.Nodes))
	for _, n := range w.Nodes {
		out = append(out, n.ID)
	}
	sort.Strings(out)
	return out
}

func arrows(w wf.Workflow) []string {
	out := make([]string, 0, len(w.Edges))
	for _, e := range w.Edges {
		out = append(out, e.From+"->"+e.To)
	}
	sort.Strings(out)
	return out
}

const child = `
name: ml_training
type: dag
image: python:3.12
steps:
  - id: train
    run: ./train.sh
  - id: evaluate
    run: ./evaluate.sh
    depends_on: [train]
`

const parent = `
name: nightly
type: dag
image: alpine
steps:
  - id: prepare
    run: ./prepare.sh
  - id: mlops
    uses: ml_training
    depends_on: [prepare]
  - id: report
    run: ./report.sh
    depends_on: [mlops]
`

// TestAUsesStepBecomesTheChildsSteps.
//
// One run, one graph, one pool. The alternative -- a step that triggers a child
// RUN and waits -- is the design that deadlocked Airflow, and this engine has
// the same ingredient: Runner.Slots is a per-process semaphore, so a parent
// holding a slot while waiting for a child that needs slots from the same pool
// hangs under load.
func TestAUsesStepBecomesTheChildsSteps(t *testing.T) {
	all, err := resolve(t, child, parent)
	if err != nil {
		t.Fatal(err)
	}
	w := find(t, all, "nightly")

	want := []string{"mlops.evaluate", "mlops.train", "prepare", "report"}
	if strings.Join(ids(w), ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, wanted %v", ids(w), want)
	}
	// The `uses` node itself is gone: what reaches the runner is flat.
	for _, n := range w.Nodes {
		if n.Uses != "" {
			t.Errorf("%s still carries a `uses`", n.ID)
		}
	}
}

// TestTheEdgesReachTheRightEndsOfTheChild.
//
// An arrow INTO the step becomes one into each of the child's roots; an arrow
// OUT of it becomes one out of each of its leaves. Getting this wrong is how
// `report` ends up running before the training finished.
func TestTheEdgesReachTheRightEndsOfTheChild(t *testing.T) {
	all, err := resolve(t, child, parent)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"mlops.evaluate->report", // the child's LEAF feeds what came after
		"mlops.train->mlops.evaluate",
		"prepare->mlops.train", // what came before feeds the child's ROOT
	}
	got := arrows(find(t, all, "nightly"))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("edges = %v, wanted %v", got, want)
	}
}

// TestTheChildsStepsKeepTheChildsImage.
//
// They land in the parent, and without materialising the child workflow's own
// defaults they would start inheriting the PARENT's image -- the same YAML
// producing a different container depending on who used it.
func TestTheChildsStepsKeepTheChildsImage(t *testing.T) {
	all, err := resolve(t, child, parent)
	if err != nil {
		t.Fatal(err)
	}
	w := find(t, all, "nightly")
	for _, n := range w.Nodes {
		switch n.ID {
		case "mlops.train", "mlops.evaluate":
			if n.Image != "python:3.12" {
				t.Errorf("%s runs in %q, not the child's image", n.ID, n.Image)
			}
		default:
			if n.Image != "" {
				t.Errorf("%s grew an image of its own: %q", n.ID, n.Image)
			}
		}
	}
}

// The child arrives as one collapsible box, which is what somebody writing
// `uses:` was drawing in their head. It composes with `group:` rather than
// needing anything of its own.
func TestTheChildArrivesAsAGroup(t *testing.T) {
	all, err := resolve(t, child, parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range find(t, all, "nightly").Nodes {
		if strings.HasPrefix(n.ID, "mlops.") && n.Group != "mlops" {
			t.Errorf("%s is in group %q", n.ID, n.Group)
		}
	}
}

// TestUsesGoesInACircleIsRefused.
//
// Without the check, a workflow that uses itself expands until the process runs
// out of memory, and the error is a stack overflow rather than the name of the
// file somebody has to fix.
func TestUsesGoesInACircleIsRefused(t *testing.T) {
	a := `
name: a
steps:
  - id: go_b
    uses: b
`
	b := `
name: b
steps:
  - id: go_a
    uses: a
`
	_, err := resolve(t, a, b)
	if err == nil {
		t.Fatal("two workflows using each other were accepted")
	}
	if !strings.Contains(err.Error(), "circle") {
		t.Errorf("the error does not say what happened: %v", err)
	}
	// And it names the chain, so the fix is not a search.
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("the error does not name the chain: %v", err)
	}
}

// A `uses:` naming something that is not in this publish is refused, listing
// what IS. Reading the database instead would make `brevis validate` -- which
// touches no database, on purpose -- answer a different question from
// `brevis publish`.
func TestAUsesOfSomethingNotInThePublishIsRefused(t *testing.T) {
	_, err := resolve(t, parent)
	if err == nil {
		t.Fatal("a uses of an absent workflow was accepted")
	}
	if !strings.Contains(err.Error(), "ml_training") || !strings.Contains(err.Error(), "nightly") {
		t.Errorf("the error names neither what is missing nor what is there: %v", err)
	}
}

// A step cannot both name another workflow and declare work of its own: that is
// a file saying two things, and expansion would have to drop one.
func TestAUsesStepThatAlsoDeclaresWorkIsRefused(t *testing.T) {
	_, err := spec.Parse("x.yaml", []byte(`
name: w
steps:
  - id: mlops
    uses: other
    run: ./also_this.sh
`))
	if err == nil {
		t.Fatal("a `uses` step with a `run` was accepted")
	}
}

// TestNestingIsFlattened: a child that itself uses a grandchild comes out flat,
// because expansion is depth first.
func TestNestingIsFlattened(t *testing.T) {
	grand := `
name: base
steps:
  - id: seed
    run: ./seed.sh
`
	mid := `
name: middle
type: dag
steps:
  - id: sub
    uses: base
  - id: after
    run: ./after.sh
    depends_on: [sub]
`
	top := `
name: top
steps:
  - id: everything
    uses: middle
`
	all, err := resolve(t, grand, mid, top)
	if err != nil {
		t.Fatal(err)
	}
	w := find(t, all, "top")
	want := []string{"everything.after", "everything.sub.seed"}
	if strings.Join(ids(w), ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, wanted %v", ids(w), want)
	}
	if strings.Join(arrows(w), ",") != "everything.sub.seed->everything.after" {
		t.Errorf("edges = %v", arrows(w))
	}
}

// A workflow with no `uses:` comes out of Resolve byte for byte what it was.
func TestAWorkflowWithoutUsesIsUntouched(t *testing.T) {
	all, err := resolve(t, child)
	if err != nil {
		t.Fatal(err)
	}
	w := find(t, all, "ml_training")
	if strings.Join(ids(w), ",") != "evaluate,train" {
		t.Errorf("steps = %v", ids(w))
	}
	if w.Nodes[0].Group != "" {
		t.Errorf("a step grew a group: %q", w.Nodes[0].Group)
	}
	// And it is still publishable on its own: expansion COPIES, it does not
	// consume.
	if len(all) != 1 {
		t.Errorf("the child disappeared from the set")
	}
}

// A child's `unless_empty:` and `for_each:` name the CHILD's steps, so they
// move with the prefix. A key that named `train` has to keep naming it after it
// becomes `mlops.train`.
func TestTheChildsContextKeysMoveWithIt(t *testing.T) {
	gated := `
name: ml_training
type: dag
steps:
  - id: train
    run: ./train.sh
  - id: evaluate
    run: ./evaluate.sh
    depends_on: [train]
    unless_empty: train.improved
`
	all, err := resolve(t, gated, parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range find(t, all, "nightly").Nodes {
		if n.ID == "mlops.evaluate" && n.UnlessEmpty != "mlops.train.improved" {
			t.Errorf("unless_empty = %q; it still names the unprefixed step", n.UnlessEmpty)
		}
	}
}
