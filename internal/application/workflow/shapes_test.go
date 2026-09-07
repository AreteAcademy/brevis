package workflow_test

import (
	"strings"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func edgeOf(t *testing.T, w wf.Workflow, from, to string) wf.Edge {
	t.Helper()
	for _, e := range w.Edges {
		if e.From == from && e.To == to {
			return e
		}
	}
	t.Fatalf("no edge %s -> %s in %v", from, to, w.Edges)
	return wf.Edge{}
}

// TestABareDependsOnStillParses is the assertion that matters most about this
// feature: every workflow ever written uses the list-of-strings form, and none
// of them may have to change.
func TestABareDependsOnStillParses(t *testing.T) {
	w, err := parse(t, `
name: w
type: dag
steps:
  - id: extract
    run: echo a
  - id: transform
    run: echo b
    depends_on: [extract]
`)
	if err != nil {
		t.Fatal(err)
	}
	e := edgeOf(t, w, "extract", "transform")
	if e.Label != "" {
		t.Errorf("a bare dependency grew a label: %q", e.Label)
	}
}

// The object form carries a label, and the two forms mix in one list -- which
// is what a real branch looks like, where only some of the arrows have anything
// to say.
func TestADependencyCanCarryALabel(t *testing.T) {
	w, err := parse(t, `
name: w
type: dag
steps:
  - id: determine_load_type
    run: ./decide.sh
  - id: internal_api_load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}
  - id: internal_api_load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
  - id: report
    run: ./report.sh
    depends_on: [internal_api_load_full, internal_api_load_delta]
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := edgeOf(t, w, "determine_load_type", "internal_api_load_full").Label; got != "additional data" {
		t.Errorf("label = %q", got)
	}
	if got := edgeOf(t, w, "determine_load_type", "internal_api_load_delta").Label; got != "changed existing data" {
		t.Errorf("label = %q", got)
	}
	// The bare entries in the same workflow are untouched.
	if got := edgeOf(t, w, "internal_api_load_full", "report").Label; got != "" {
		t.Errorf("an unlabelled edge got %q", got)
	}
}

// A label with no step is a file that says something about a dependency it
// never names. Refused rather than dropped, because dropping it produces a
// graph missing an arrow the author believed they had drawn.
func TestALabelWithNoStepIsRefused(t *testing.T) {
	_, err := parse(t, `
name: w
type: dag
steps:
  - id: a
    run: echo a
  - id: b
    run: echo b
    depends_on:
      - {label: changed existing data}
`)
	if err == nil {
		t.Fatal("a dependency with a label and no step was accepted")
	}
}

// TestAMarkerNeedsNoCommand: the EmptyOperator every orchestrator ends up with.
func TestAMarkerNeedsNoCommand(t *testing.T) {
	w, err := parse(t, `
name: w
type: dag
steps:
  - id: start
    marker: true
  - id: work
    run: ./work.sh
    depends_on: [start]
  - id: end
    marker: true
    depends_on: [work]
`)
	if err != nil {
		t.Fatalf("a marker was refused: %v", err)
	}
	if !w.Nodes[0].Marker {
		t.Error("marker did not survive the parse")
	}
}

// TestAnAccidentalEmptyStepIsStillRefused.
//
// The two cases must not collapse into one. A step with no `run:` is almost
// always a mistake -- a YAML key mistyped, a template that produced nothing --
// and `marker: true` is how somebody says they meant it. If markers made the
// check disappear, this feature would have cost a real error message.
func TestAnAccidentalEmptyStepIsStillRefused(t *testing.T) {
	_, err := parse(t, `
name: w
steps:
  - id: oops
`)
	if err == nil {
		t.Fatal("a step with no run and no action was accepted")
	}
	// And the message points at the way to mean it.
	if !strings.Contains(err.Error(), "marker") {
		t.Errorf("the error does not mention markers: %v", err)
	}
}

// A marker that also declares work is a file saying two things. Preferring one
// silently loses whatever the author wrote in the other.
func TestAMarkerThatAlsoDeclaresWorkIsRefused(t *testing.T) {
	_, err := parse(t, `
name: w
steps:
  - id: start
    marker: true
    run: ./actually_does_something.sh
`)
	if err == nil {
		t.Fatal("a marker with a `run:` was accepted")
	}
}

// TestABareUnlessEmptyKeyIsRefused.
//
// The key is always qualified by the step that publishes it, the same rule the
// context library carries. Steps are isolated -- `extract` and `transform` can
// both publish `has_rows` and neither loses it -- and a bare key throws that
// away the moment two of them do.
func TestABareUnlessEmptyKeyIsRefused(t *testing.T) {
	_, err := parse(t, `
name: w
type: dag
steps:
  - id: extract
    run: ./extract.sh
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: has_rows
`)
	if err == nil {
		t.Fatal("a bare key was accepted")
	}
	if !strings.Contains(err.Error(), "extract.has_rows") {
		t.Errorf("the error does not show the shape it wants: %v", err)
	}
}

// TestReadingAStepItDoesNotDependOnIsRefusedAtPublish.
//
// A step gated on a key it cannot see would be skipped forever, silently.
// Finding that at publish beats finding it out when a nightly stops running.
func TestReadingAStepItDoesNotDependOnIsRefusedAtPublish(t *testing.T) {
	_, err := parse(t, `
name: w
type: dag
steps:
  - id: extract
    run: ./extract.sh
  - id: sibling
    run: ./sibling.sh
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: sibling.has_rows
`)
	if err == nil {
		t.Fatal("a step read a key from something it does not depend on")
	}
	// It lists what the step CAN see, so the fix is one line away.
	if !strings.Contains(err.Error(), "extract") {
		t.Errorf("the error does not list what is visible: %v", err)
	}
}

// A key from a TRANSITIVE dependency is fine, and it is the natural pipeline:
// load depends on transform depends on extract, and load legitimately wants
// what extract published.
func TestATransitiveDependencyIsVisible(t *testing.T) {
	w, err := parse(t, `
name: w
type: dag
steps:
  - id: extract
    run: ./extract.sh
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
  - id: load
    run: ./load.sh
    depends_on: [transform]
    unless_empty: extract.has_rows
`)
	if err != nil {
		t.Fatalf("a transitive key was refused: %v", err)
	}
	if w.Nodes[2].UnlessEmpty != "extract.has_rows" {
		t.Errorf("unless_empty did not survive the parse: %q", w.Nodes[2].UnlessEmpty)
	}
}

// A step with `unless_empty:` and no dependencies at all can see nothing, and
// the message says that rather than listing an empty set.
func TestAGateWithNoDependenciesSaysSo(t *testing.T) {
	_, err := parse(t, `
name: w
type: dag
steps:
  - id: lonely
    run: ./lonely.sh
    unless_empty: nobody.has_rows
`)
	if err == nil {
		t.Fatal("a step with no depends_on read a key")
	}
	if !strings.Contains(err.Error(), "depends_on") {
		t.Errorf("the error does not point at the missing depends_on: %v", err)
	}
}
