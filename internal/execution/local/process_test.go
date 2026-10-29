package local

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

// The executor's boundary is code, not convention. If this test starts failing,
// something has crossed it: the local executor must not be constructible
// outside local mode.
func TestItRefusesToBeBuiltOutsideLocal(t *testing.T) {
	for _, env := range []string{"prod", "staging", "dev", ""} {
		if _, err := New(env); err == nil {
			t.Fatalf("New(%q) should refuse: outside local, `run:` goes to Kubernetes", env)
		} else if !errors.As(err, &ErrOutsideLocal{}) {
			t.Errorf("New(%q) devolveu %T, wanted ErrOutsideLocal", env, err)
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
	events := collectOutput(t, ev)

	if events[0].Kind != execution.EventStarted {
		t.Errorf("first event = %v, wanted started", events[0].Kind)
	}
	last := events[len(events)-1]
	if last.Kind != execution.EventSucceeded {
		t.Errorf("last event = %v, wanted succeeded", last.Kind)
	}
	if !hasLog(events, "ola", "stdout") {
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
	events := collectOutput(t, ev)

	if !hasLog(events, "out", "stdout") {
		t.Error("stdout was not classified")
	}
	if !hasLog(events, "err", "stderr") {
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
	events := collectOutput(t, ev)

	last := events[len(events)-1]
	if last.Kind != execution.EventFailed {
		t.Fatalf("last event = %v, wanted failed", last.Kind)
	}
	if last.ExitCode != 3 {
		t.Errorf("exit = %d, wanted 3", last.ExitCode)
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
	last := collectOutput(t, ev)
	if k := last[len(last)-1].Kind; k != execution.EventFailed {
		t.Errorf("last event = %v, wanted failed after the cancellation", k)
	}
}

func hasLog(events []execution.Event, msg, stream string) bool {
	for _, e := range events {
		if e.Kind == execution.EventLog && e.Stream == stream && strings.Contains(e.Message, msg) {
			return true
		}
	}
	return false
}

// A STEP IS STOPPED WITH EVERYTHING ITS SHELL STARTED. Killing only the `sh -c`
// left its child orphaned, holding stdout: the step did not end until the
// child did, and a run canceled from the console stayed `running` for as long
// as the child's sleep.
func TestStoppingAStepKillsWhatItsShellStarted(t *testing.T) {
	p := executor(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	ev, err := p.Execute(ctx, execution.TaskExec{
		ExecutionID: "6", NodeID: "n", Command: "sleep 30 & echo $!; wait",
	})
	if err != nil {
		t.Fatal(err)
	}

	var child int
	for e := range ev {
		if e.Kind == execution.EventLog {
			if child, err = strconv.Atoi(strings.TrimSpace(e.Message)); err != nil {
				t.Fatalf("the child's pid: %v", err)
			}
			break
		}
	}
	stop()

	ended := make(chan struct{})
	go func() { defer close(ended); collectOutput(t, ev) }()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the step had not ended 5s after it was stopped; its child holds stdout")
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(child, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(child, syscall.SIGKILL)
			t.Fatalf("the shell's child %d outlived the step", child)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
