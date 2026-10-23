package execution_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/agent"
	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// #127, end to end: two runs of ONE workflow, at the same time, with a `host:`
// step on ONE real agent. The agent refuses an execution id it already runs,
// and the id used to name the step -- "daily:extract" -- so the second run
// failed at once: "already running here; a retry gets a new id". A backfill of
// two days was enough.
func TestTwoRunsOfOneWorkflowShareAnAgent(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	srv := httptest.NewServer(a.Handler(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	hosts := map[string]execution.Executor{"box": &remote.Executor{
		Host: "box", Agent: remote.HTTPAgent{BaseURL: srv.URL}, LeaseLimit: 10 * time.Second,
	}}
	w := wf.Workflow{Slug: "daily", Nodes: []wf.Node{
		{ID: "extract", Host: "box", Run: "sleep 1"},
	}}

	// The second run starts while the first one's step is RUNNING on the
	// agent, which is how two runs meet in practice -- a backfill, a click
	// during a schedule. Starting both in the same instant would only test
	// the agent's check-then-insert window, where both slip through (#127).
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = app.Runner{RunID: uuid.New(), Report: &coletor{}, Hosts: hosts}.
				Run(context.Background(), w)
		}()
		time.Sleep(300 * time.Millisecond)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d of two concurrent ones failed on the shared agent: %v", i, err)
		}
	}
}
