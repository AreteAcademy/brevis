package local_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

func collectEvents(ev <-chan execution.Event) []execution.Event {
	var out []execution.Event
	for e := range ev {
		out = append(out, e)
	}
	return out
}

func lastKind(ev []execution.Event) execution.EventKind {
	return ev[len(ev)-1].Kind
}

func TestTheGoExecutorRunsARegisteredTask(t *testing.T) {
	reg := execution.NewRegistry()
	var ran atomic.Bool
	reg.MustRegister(execution.FuncTask{TaskName: "sync", Fn: func(_ context.Context, in execution.Input) error {
		ran.Store(true)
		in.Log("sincronizando")
		return nil
	}})

	ev, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(ev)

	if !ran.Load() {
		t.Error("the task did not run")
	}
	if lastKind(events) != execution.EventSucceeded {
		t.Errorf("last event = %v", lastKind(events))
	}
	var hasLog bool
	for _, e := range events {
		if e.Kind == execution.EventLog && e.Message == "sincronizando" {
			hasLog = true
		}
	}
	if !hasLog {
		t.Error("the task's Log did not become an event")
	}
}

// The error should list what exists: it saves a trip to the documentation and
// gives a typo away at once.
func TestTheGoExecutorListsTheAvailableTasksForAnUnknownOne(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{TaskName: "daily_sync", Fn: func(context.Context, execution.Input) error { return nil }})

	_, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "daly_sync"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "daily_sync") {
		t.Errorf("error = %q; it should list the available tasks", err)
	}
}

// A task runs in the SAME process, unlike a pod: a panic must not
// derrubar o orquestrador junto.
func TestTheGoExecutorContainsAPanic(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{TaskName: "explode", Fn: func(context.Context, execution.Input) error {
		panic("boom")
	}})

	ev, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "explode"})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(ev)

	if lastKind(events) != execution.EventFailed {
		t.Fatalf("last event = %v, wanted failed", lastKind(events))
	}
	if !strings.Contains(events[len(events)-1].Err.Error(), "panico") {
		t.Errorf("error = %v; it should name the panic", events[len(events)-1].Err)
	}
}

func TestTheGoExecutorRespectsTheTimeout(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{TaskName: "lenta", Fn: func(ctx context.Context, _ execution.Input) error {
		select {
		case <-time.After(5 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})

	start := time.Now()
	ev, err := local.NewGoExecutor(reg).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "1", NodeID: "n", Action: "lenta", Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(ev)

	if d := time.Since(start); d > time.Second {
		t.Errorf("it took %s; the timeout did not interrupt", d)
	}
	last := events[len(events)-1]
	if last.Kind != execution.EventFailed {
		t.Fatalf("last event = %v, wanted failed", last.Kind)
	}
	if !strings.Contains(last.Message, "timeout") {
		t.Errorf("message = %q; it should mention the timeout", last.Message)
	}
}

func TestTheGoExecutorPropagatesTheTasksError(t *testing.T) {
	reg := execution.NewRegistry()
	failure1 := errors.New("source unavailable")
	reg.MustRegister(execution.FuncTask{TaskName: "falha", Fn: func(context.Context, execution.Input) error {
		return failure1
	}})

	ev, _ := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "falha"})
	events := collectEvents(ev)

	if !errors.Is(events[len(events)-1].Err, failure1) {
		t.Error("the task's error did not reach the event")
	}
}
