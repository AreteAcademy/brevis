package agent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// One step source, two executors, the same published context.
//
// The point of the whole exercise: "point your existing steps at a pod you
// run" is only true if a step does not have to know where it runs. The
// kubernetes executor is the third and needs a cluster, so it is NOT here —
// it has its own tests and this file says which two it compares rather than
// implying three.
//
// They report it by DIFFERENT roads, which is why it has to be checked rather
// than assumed: the local executor reads the file and emits EventContext; the
// agent turns the file into the `@brevis:` marker, which the runner reads from
// the log stream. Two roads, one answer.
func TestLocalAndAHostPublishTheSameThing(t *testing.T) {
	const step = `echo '{"rows":7,"watermark":"2026-10-05"}' > "$BREVIS_OUTPUT"`

	if got, want := hostPublishes(t, step, 0), `{"rows":7,"watermark":"2026-10-05"}`; got != want {
		t.Errorf("a host published %s, want %s", got, want)
	}
	if got, want := localPublishes(t, step, 0), `{"rows":7,"watermark":"2026-10-05"}`; got != want {
		t.Errorf("local published %s, want %s", got, want)
	}
}

// And both read it on FAILURE, which is a deliberate property of the local
// executor and had to become one of the agent rather than a coincidence.
//
// Its comment says why: a step that publishes and then fails has said
// something true up to that point, and reading only on success throws away the
// one clue a failing step left behind.
func TestBothPublishWhenTheStepThenFails(t *testing.T) {
	const step = `echo '{"got_this_far":true}' > "$BREVIS_OUTPUT"; exit 3`

	if got := hostPublishes(t, step, 3); !strings.Contains(got, "got_this_far") {
		t.Errorf("a host published %q after a failing step", got)
	}
	if got := localPublishes(t, step, 3); !strings.Contains(got, "got_this_far") {
		t.Errorf("local published %q after a failing step", got)
	}
}

// hostPublishes runs the step on a real agent and reads what came back.
func hostPublishes(t *testing.T, command string, wantCode int) string {
	t.Helper()
	e := pair(t, agent.Options{})
	tk := task("agree-host", command)
	events := run(t, e, tk)
	assertCode(t, events, wantCode)
	return strings.TrimSpace(publishedBy(t, events))
}

// localPublishes runs the SAME source on the local executor, where the RUNNER
// is what picks the path -- so the test picks it, as the runner would.
func localPublishes(t *testing.T, command string, wantCode int) string {
	t.Helper()
	e, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	tk := task("agree-local", command)
	tk.OutputPath = filepath.Join(t.TempDir(), "out.json")
	tk.Env = map[string]string{"BREVIS_OUTPUT": tk.OutputPath}

	ch, err := e.Execute(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch)
	assertCode(t, events, wantCode)

	for _, ev := range events {
		if ev.Kind == execution.EventContext {
			return strings.TrimSpace(ev.Message)
		}
	}
	t.Fatalf("local published nothing; the stream was:\n%s", strings.Join(logs(events), "\n"))
	return ""
}

func assertCode(t *testing.T, events []execution.Event, want int) {
	t.Helper()
	got := last(events)
	if want == 0 {
		if got.Kind != execution.EventSucceeded {
			t.Fatalf("the step ended %s, want succeeded", got.Kind)
		}
		return
	}
	if got.Kind != execution.EventFailed || got.ExitCode != want {
		t.Fatalf("the step ended %s with code %d, want failed with %d",
			got.Kind, got.ExitCode, want)
	}
}
