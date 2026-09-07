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
	var rodou atomic.Bool
	reg.MustRegister(execution.FuncTask{Nome: "sync", Fn: func(_ context.Context, in execution.Input) error {
		rodou.Store(true)
		in.Log("sincronizando")
		return nil
	}})

	ev, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	eventos := collectEvents(ev)

	if !rodou.Load() {
		t.Error("the task did not run")
	}
	if lastKind(eventos) != execution.EventSucceeded {
		t.Errorf("last event = %v", lastKind(eventos))
	}
	var hasLog bool
	for _, e := range eventos {
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
	reg.MustRegister(execution.FuncTask{Nome: "daily_sync", Fn: func(context.Context, execution.Input) error { return nil }})

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
func TestGoExecutorContemPanico(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "explode", Fn: func(context.Context, execution.Input) error {
		panic("boom")
	}})

	ev, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "explode"})
	if err != nil {
		t.Fatal(err)
	}
	eventos := collectEvents(ev)

	if lastKind(eventos) != execution.EventFailed {
		t.Fatalf("last event = %v, wanted failed", lastKind(eventos))
	}
	if !strings.Contains(eventos[len(eventos)-1].Err.Error(), "panico") {
		t.Errorf("error = %v; it should name the panic", eventos[len(eventos)-1].Err)
	}
}

func TestTheGoExecutorRespectsTheTimeout(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "lenta", Fn: func(ctx context.Context, _ execution.Input) error {
		select {
		case <-time.After(5 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})

	inicio := time.Now()
	ev, err := local.NewGoExecutor(reg).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "1", NodeID: "n", Action: "lenta", Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := collectEvents(ev)

	if d := time.Since(inicio); d > time.Second {
		t.Errorf("it took %s; the timeout did not interrupt", d)
	}
	ultimo := eventos[len(eventos)-1]
	if ultimo.Kind != execution.EventFailed {
		t.Fatalf("last event = %v, wanted failed", ultimo.Kind)
	}
	if !strings.Contains(ultimo.Message, "timeout") {
		t.Errorf("message = %q; it should mention the timeout", ultimo.Message)
	}
}

func TestTheGoExecutorPropagatesTheTasksError(t *testing.T) {
	reg := execution.NewRegistry()
	falha := errors.New("source unavailable")
	reg.MustRegister(execution.FuncTask{Nome: "falha", Fn: func(context.Context, execution.Input) error {
		return falha
	}})

	ev, _ := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "falha"})
	eventos := collectEvents(ev)

	if !errors.Is(eventos[len(eventos)-1].Err, falha) {
		t.Error("the task's error did not reach the event")
	}
}
