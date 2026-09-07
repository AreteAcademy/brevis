package local

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

func executor(t *testing.T) *ProcessExecutor {
	t.Helper()
	p, err := New("local")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func collectOutput(t *testing.T, ev <-chan execution.Event) []execution.Event {
	t.Helper()
	var out []execution.Event
	for e := range ev {
		out = append(out, e)
	}
	return out
}

// The executor's boundary is code, not convention. If this test starts
// falhar, a emenda a secao 3 do plano foi violada.
func TestItRefusesToBeBuiltOutsideLocal(t *testing.T) {
	for _, env := range []string{"prod", "staging", "dev", ""} {
		if _, err := New(env); err == nil {
			t.Fatalf("New(%q) should refuse: outside local, `run:` goes to Kubernetes", env)
		} else if !errors.As(err, &ErrForaDoLocal{}) {
			t.Errorf("New(%q) devolveu %T, wanted ErrForaDoLocal", env, err)
		}
	}
}

func TestItRunsAndReportsSuccess(t *testing.T) {
	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "1", NodeID: "hello", Command: "echo ola",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := collectOutput(t, ev)

	if eventos[0].Kind != execution.EventStarted {
		t.Errorf("first event = %v, wanted started", eventos[0].Kind)
	}
	ultimo := eventos[len(eventos)-1]
	if ultimo.Kind != execution.EventSucceeded {
		t.Errorf("last event = %v, wanted succeeded", ultimo.Kind)
	}
	if !hasLog(eventos, "ola", "stdout") {
		t.Error("it did not capture the command's output")
	}
}

// stdout and stderr have to arrive separately: merging them loses where the
// message came from, which is what made dbt's summary show up as an error in
// Leoflow.
func TestItSeparatesStdoutFromStderr(t *testing.T) {
	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "2", NodeID: "n", Command: "echo out; echo err >&2",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := collectOutput(t, ev)

	if !hasLog(eventos, "out", "stdout") {
		t.Error("stdout was not classified")
	}
	if !hasLog(eventos, "err", "stderr") {
		t.Error("stderr was not classified")
	}
}

func TestItReportsAFailureWithTheExitCode(t *testing.T) {
	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "3", NodeID: "n", Command: "exit 3",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := collectOutput(t, ev)

	ultimo := eventos[len(eventos)-1]
	if ultimo.Kind != execution.EventFailed {
		t.Fatalf("last event = %v, wanted failed", ultimo.Kind)
	}
	if ultimo.ExitCode != 3 {
		t.Errorf("exit = %d, wanted 3", ultimo.ExitCode)
	}
}

// The environment is explicit: the orchestrator carries credentials a task must
// not get to see by accident.
func TestItDoesNotInheritTheParentsEnvironment(t *testing.T) {
	t.Setenv("SEGREDO_DO_ORQUESTRADOR", "nao-vazar")

	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "4", NodeID: "n",
		Command: "echo [$SEGREDO_DO_ORQUESTRADOR]",
		Env:     map[string]string{"PERMITIDA": "sim"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range collectOutput(t, ev) {
		if strings.Contains(e.Message, "nao-vazar") {
			t.Fatal("the task could see a variable of the parent process")
		}
	}
}

func TestCancelInterrompe(t *testing.T) {
	p := executor(t)
	ev, err := p.Execute(context.Background(), execution.TaskExec{
		ExecutionID: "5", NodeID: "n", Command: "sleep 30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	ultimo := collectOutput(t, ev)
	if k := ultimo[len(ultimo)-1].Kind; k != execution.EventFailed {
		t.Errorf("last event = %v, wanted failed after the cancellation", k)
	}
}

func hasLog(eventos []execution.Event, msg, stream string) bool {
	for _, e := range eventos {
		if e.Kind == execution.EventLog && e.Stream == stream && strings.Contains(e.Message, msg) {
			return true
		}
	}
	return false
}
