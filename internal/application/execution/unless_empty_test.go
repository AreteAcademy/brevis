package execution_test

import (
	"context"
	"os"
	"strings"
	"testing"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
	"github.com/google/uuid"
)

// gated builds extract -> transform, where extract publishes `body` and
// transform runs only unless_empty says otherwise. It reports whether transform
// ran.
func gated(t *testing.T, body, key string) (bool, *spyPersister, error) {
	t.Helper()
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := dir + "/transform-ran"

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: `sh -c 'printf ` + "'\"'\"'" + body + "'\"'\"'" + ` > "$BREVIS_OUTPUT"'`},
			{ID: "transform", Run: `sh -c 'touch ` + marker + `'`, UnlessEmpty: key},
		},
		Edges: []wf.Edge{{From: "extract", To: "transform"}},
	}
	spy := &spyPersister{}
	runErr := app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: spy, RunID: uuid.New(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)

	_, err = os.Stat(marker)
	return err == nil, spy, runErr
}

// A truthy value runs the step. The half that would pass with the feature
// missing entirely, so it is worth little on its own.
func TestAPresentValueRunsTheStep(t *testing.T) {
	ran, _, err := gated(t, `{"has_rows":true}`, "extract.has_rows")
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("the step did not run with a true value")
	}
}

// TestAnEmptyValueSkipsTheStep is the feature.
func TestAnEmptyValueSkipsTheStep(t *testing.T) {
	for _, body := range []string{
		`{"has_rows":false}`, `{"has_rows":0}`, `{"has_rows":""}`,
		`{"has_rows":null}`, `{"has_rows":[]}`, `{"has_rows":{}}`,
	} {
		t.Run(body, func(t *testing.T) {
			ran, spy, err := gated(t, body, "extract.has_rows")
			if err != nil {
				t.Fatalf("the run failed: %v", err)
			}
			if ran {
				t.Errorf("%s did not count as empty", body)
			}
			if reason := spy.skips()["transform"]; !strings.Contains(reason, "extract.has_rows") {
				t.Errorf("the reason does not name the key: %q", reason)
			}
		})
	}
}

// A non-empty value of any shape runs the step. It is the other half of the
// list above, and without it "empty" could mean "anything".
func TestANonEmptyValueOfAnyShapeRuns(t *testing.T) {
	for _, body := range []string{
		`{"has_rows":true}`, `{"has_rows":1}`, `{"has_rows":"yes"}`,
		`{"has_rows":[1]}`, `{"has_rows":{"a":1}}`,
		// A STRING "false" is a non-empty string. A step that published those
		// five characters published something, and guessing it meant a boolean
		// is how a rule starts having opinions its author cannot see.
		`{"has_rows":"false"}`,
	} {
		t.Run(body, func(t *testing.T) {
			ran, _, err := gated(t, body, "extract.has_rows")
			if err != nil {
				t.Fatal(err)
			}
			if !ran {
				t.Errorf("%s counted as empty", body)
			}
		})
	}
}

// TestAMissingKeyFailsRatherThanSkipping is the decision in this feature.
//
// A missing key could be read as "empty, so skip" -- and a typo in the key name
// would then disable the step silently and forever, with nothing anywhere
// saying why. A nightly that stops running and leaves no trace is the worst
// outcome this can have, so it fails loudly instead.
func TestAMissingKeyFailsRatherThanSkipping(t *testing.T) {
	ran, spy, err := gated(t, `{"has_rows":true}`, "extract.has_row")
	if err == nil {
		t.Fatal("a typo in the key silently skipped the step")
	}
	if ran {
		t.Error("the step ran on a key that does not exist")
	}
	if _, skipped := spy.skips()["transform"]; skipped {
		t.Error("a missing key was recorded as a skip, which reads as a decision")
	}
	// The message has to name the key AND what was actually published, or
	// finding the typo is an investigation instead of a glance.
	if !strings.Contains(err.Error(), "extract.has_row") ||
		!strings.Contains(err.Error(), `"has_rows"`) {
		t.Errorf("the error does not point at the typo: %v", err)
	}
}

// A publisher that ran and wrote nothing at all gets its own message. It is a
// different mistake from a typo and the fix is different too.
func TestAPublisherThatWroteNothingSaysSo(t *testing.T) {
	_, _, err := gated(t, ``, "extract.has_rows")
	if err == nil {
		t.Fatal("a step read from a publisher that published nothing, and ran")
	}
	if !strings.Contains(err.Error(), "published nothing") {
		t.Errorf("the message does not say what happened: %v", err)
	}
}

// A step with no `unless_empty:` is untouched, which is every step written
// before this existed.
func TestAStepWithNoGateIsUntouched(t *testing.T) {
	ran, spy, err := gated(t, `{"anything":1}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("a step with no gate did not run")
	}
	if len(spy.skips()) != 0 {
		t.Errorf("something was skipped: %v", spy.skips())
	}
}
