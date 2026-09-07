package workflow_test

import (
	"strings"
	"testing"

	spec "github.com/AreteAcademy/brevis/internal/application/workflow"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func parse(t *testing.T, yaml string) (wf.Workflow, error) {
	t.Helper()
	return spec.Parse("alerts.yaml", []byte(yaml))
}

const withOnError = `
name: id_verification
steps:
  - id: extract
    run: python fetch.py
    on_error:
      type: %s
`

// TestAnUnknownChannelIsRefusedAtPublish.
//
// The whole point of a closed vocabulary: an unknown channel is refused when
// the workflow is published, naming what is valid, rather than discovered on
// the night the alert was needed -- which is the only night it matters.
func TestAnUnknownChannelIsRefusedAtPublish(t *testing.T) {
	_, err := parse(t, strings.Replace(withOnError, "%s", "TEAMS", 1))
	if err == nil {
		t.Fatal("`type: TEAMS` was accepted and nothing delivers to it")
	}
	// It has to name what IS valid. "Unknown channel" alone sends the reader to
	// the source.
	if !strings.Contains(err.Error(), wf.ChannelSlack) {
		t.Errorf("the error does not say what is valid: %v", err)
	}
}

func TestAKnownChannelIsAccepted(t *testing.T) {
	w, err := parse(t, strings.Replace(withOnError, "%s", wf.ChannelSlack, 1))
	if err != nil {
		t.Fatal(err)
	}
	if w.Nodes[0].OnError == nil || w.Nodes[0].OnError.Type != wf.ChannelSlack {
		t.Fatalf("on_error did not survive the parse: %+v", w.Nodes[0].OnError)
	}
}

// A typo in case is a typo, not a different channel. Refusing `slack` would be
// pedantry with a four-in-the-morning cost.
func TestTheChannelIsCaseInsensitive(t *testing.T) {
	w, err := parse(t, strings.Replace(withOnError, "%s", "slack", 1))
	if err != nil {
		t.Fatalf("`type: slack` was refused: %v", err)
	}
	if w.Nodes[0].OnError.Type != wf.ChannelSlack {
		t.Errorf("Type = %q", w.Nodes[0].OnError.Type)
	}
}

// An on_error with no type is a step asking to be announced somewhere
// unspecified, which is a file that reads as configured and is not.
func TestAnOnErrorWithNoTypeIsRefused(t *testing.T) {
	_, err := parse(t, `
name: w
steps:
  - id: extract
    run: echo hi
    on_error:
      when: attempt
`)
	if err == nil {
		t.Fatal("`on_error` with no `type` was accepted")
	}
}

func TestAnUnknownWhenIsRefused(t *testing.T) {
	_, err := parse(t, `
name: w
steps:
  - id: extract
    run: echo hi
    on_error:
      type: SLACK
      when: sometimes
`)
	if err == nil {
		t.Fatal("`when: sometimes` was accepted")
	}
	if !strings.Contains(err.Error(), wf.OnGiveUp) || !strings.Contains(err.Error(), wf.OnAttempt) {
		t.Errorf("the error does not say what is valid: %v", err)
	}
}

// TestTheDestinationCannotComeFromTheFile.
//
// A webhook is a credential -- whoever holds it posts in the channel as if they
// were the platform -- and a workflow file is written by somebody who is not
// necessarily allowed to choose where the company's alerts go. This is the same
// argument that made BREVIS_POD_ALLOWED_SECRETS a list the installation
// controls.
//
// The check is that the field does not EXIST: an unknown key in the YAML is
// ignored, so a file that tries this parses fine and quietly sends nowhere.
// What this asserts is that the domain has no place to put it.
func TestTheDestinationCannotComeFromTheFile(t *testing.T) {
	w, err := parse(t, `
name: w
steps:
  - id: extract
    run: echo hi
    on_error:
      type: SLACK
      webhook: https://hooks.slack.com/services/attacker
`)
	if err != nil {
		t.Fatal(err)
	}
	// Reflection would be indirect. The direct statement is that OnError has
	// exactly two fields, and adding a third is what this test is here to make
	// somebody think about.
	on := *w.Nodes[0].OnError
	if on != (wf.OnError{Type: wf.ChannelSlack}) {
		t.Errorf("on_error carries something beyond type and when: %+v", on)
	}
}

// The default is the quiet one, and the reason is whose night it is: a step
// that fails four times and passes on the fifth would send four messages under
// the other default.
func TestTheDefaultIsOnlyWhenItGivesUp(t *testing.T) {
	w, err := parse(t, strings.Replace(withOnError, "%s", "SLACK", 1))
	if err != nil {
		t.Fatal(err)
	}
	on := w.Nodes[0].OnError
	if on.Fires(false) {
		t.Error("the default announced a failed attempt that will be retried")
	}
	if !on.Fires(true) {
		t.Error("the default did not announce the run giving up")
	}
}

func TestWhenAttemptAnnouncesEveryFailure(t *testing.T) {
	w, err := parse(t, `
name: w
steps:
  - id: extract
    run: echo hi
    on_error: {type: SLACK, when: attempt}
`)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Nodes[0].OnError.Fires(false) {
		t.Error("`when: attempt` did not announce a retried failure")
	}
}

// A step with no on_error keeps costing nothing, which is most steps.
func TestAStepWithoutOnErrorAnnouncesNothing(t *testing.T) {
	w, err := parse(t, `
name: w
steps:
  - id: extract
    run: echo hi
`)
	if err != nil {
		t.Fatal(err)
	}
	if w.Nodes[0].OnError != nil {
		t.Fatal("on_error appeared out of nowhere")
	}
	if w.Nodes[0].OnError.Fires(true) {
		t.Error("a nil on_error fired")
	}
}
