package execution_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// capturer keeps the TaskExec the runner assembled, so the environment reaching
// the step can be checked.
type capturer struct {
	task execution.TaskExec
}

func (c *capturer) Name() string { return "capturer" }

func (c *capturer) Cancel(ctx context.Context, execID string) error { return nil }

func (c *capturer) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	c.task = t
	ch := make(chan execution.Event, 2)
	ch <- execution.Event{Kind: execution.EventStarted}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch, nil
}

// history answers whatever the test tells it to.
type historico struct {
	jaTeve bool
	err    error
	slug   string
	nodeID string
	exceto uuid.UUID
}

func (h *historico) StepHasSucceeded(ctx context.Context, slug, nodeID string, exceto uuid.UUID) (bool, error) {
	h.slug, h.nodeID, h.exceto = slug, nodeID, exceto
	return h.jaTeve, h.err
}

func oneStepWorkflow() wf.Workflow {
	return wf.Workflow{
		Slug:  "clima",
		Nodes: []wf.Node{{ID: "collectOutput", Run: "true"}},
	}
}

func runStep(t *testing.T, r app.Runner) execution.TaskExec {
	t.Helper()
	cap := &capturer{}
	r.Processo = cap
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return cap.task
}

func TestTheStepsEnvironmentCarriesTheRunsContext(t *testing.T) {
	id := uuid.New()
	when := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

	task := runStep(t, app.Runner{
		RunID:       id,
		Params:      map[string]string{"load_full": "true"},
		Trigger:     "backfill",
		LogicalDate: &when,
		History:     &historico{jaTeve: false},
	})

	if task.Env["BREVIS_RUN_ID"] != id.String() {
		t.Errorf("BREVIS_RUN_ID = %q", task.Env["BREVIS_RUN_ID"])
	}
	if task.Env["BREVIS_RUN_FIRST"] != "true" {
		t.Errorf("with no previous success the step runs for the first time: %q", task.Env["BREVIS_RUN_FIRST"])
	}
	if task.Env["BREVIS_RUN_TRIGGER"] != "backfill" {
		t.Errorf("BREVIS_RUN_TRIGGER = %q", task.Env["BREVIS_RUN_TRIGGER"])
	}
	if task.Env["BREVIS_RUN_LOGICAL_DATE"] != "2026-09-03T00:00:00Z" {
		t.Errorf("BREVIS_RUN_LOGICAL_DATE = %q", task.Env["BREVIS_RUN_LOGICAL_DATE"])
	}

	var params map[string]string
	if err := json.Unmarshal([]byte(task.Env["BREVIS_RUN_PARAMS"]), &params); err != nil {
		t.Fatalf("BREVIS_RUN_PARAMS is not JSON: %v", err)
	}
	if params["load_full"] != "true" {
		t.Errorf("params = %v", params)
	}
}

func TestTheFirstRunIsPerStepNotPerWorkflow(t *testing.T) {
	// A workflow with three fetchers writing into three tables would create only
	// the first step's if the question were about the whole workflow.
	h := &historico{jaTeve: false}
	id := uuid.New()

	runStep(t, app.Runner{RunID: id, History: h})

	if h.slug != "clima" || h.nodeID != "collectOutput" {
		t.Errorf("the question has to be per (workflow, step): %q / %q", h.slug, h.nodeID)
	}
	// The current run itself must not count as a previous success.
	if h.exceto != id {
		t.Errorf("the current run has to be excluded from the query: %v", h.exceto)
	}
}

func TestAStepWithAPreviousSuccessIsNotTheFirst(t *testing.T) {
	task := runStep(t, app.Runner{RunID: uuid.New(), History: &historico{jaTeve: true}})

	if task.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Errorf("there was a success already, so it is not the first: %q", task.Env["BREVIS_RUN_FIRST"])
	}
}

func TestWithNoHistoryItInventsNoFirstRun(t *testing.T) {
	// Creating a table without being sure is worse than not creating it:
	// whoever wants one asks for it explicitly in the fetcher's code.
	task := runStep(t, app.Runner{RunID: uuid.New()})

	if task.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Errorf("with no history configured the answer is no: %q", task.Env["BREVIS_RUN_FIRST"])
	}
}

func TestAQueryFailureDoesNotBecomeATableCreation(t *testing.T) {
	h := &historico{err: context.DeadlineExceeded}
	task := runStep(t, app.Runner{RunID: uuid.New(), History: h})

	if task.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Errorf("a database that is down must not become DDL: %q", task.Env["BREVIS_RUN_FIRST"])
	}
}

func TestTheRunnersEnvironmentWinsACollision(t *testing.T) {
	// If somebody set the variable in the runner's configuration, they meant to.
	task := runStep(t, app.Runner{
		RunID:   uuid.New(),
		History: &historico{jaTeve: false},
		Env:     map[string]string{"BREVIS_RUN_FIRST": "false", "OUTRA": "coisa"},
	})

	if task.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Error("the runner's explicit configuration has to beat the computed value")
	}
	if task.Env["OUTRA"] != "coisa" {
		t.Error("the rest of the runner's environment has to survive")
	}
	if task.Env["BREVIS_RUN_ID"] == "" {
		t.Error("the variables with no collision still arrive")
	}
}

func TestWithNoParamsItInjectsNoEmptyVariable(t *testing.T) {
	task := runStep(t, app.Runner{RunID: uuid.New(), History: &historico{}})

	if _, existe := task.Env["BREVIS_RUN_PARAMS"]; existe {
		t.Error("with no params the variable should not exist rather than exist empty")
	}
	if _, existe := task.Env["BREVIS_RUN_TRIGGER"]; existe {
		t.Error("same for no trigger")
	}
}

func TestWithNoRunIDItInventsNoManagedRun(t *testing.T) {
	// `brevis run` executes a YAML on the spot and belongs to no history.
	// The SDK decides it is under the engine by the PRESENCE of the id, so
	// injecting the zero UUID would make a hand-run fetcher log "running under
	// Brevis" with
	// um id inventado.
	task := runStep(t, app.Runner{
		Params:  map[string]string{"create_table": "true"},
		History: &historico{jaTeve: false},
	})

	for _, v := range []string{"BREVIS_RUN_ID", "BREVIS_RUN_FIRST", "BREVIS_RUN_ATTEMPT"} {
		if _, existe := task.Env[v]; existe {
			t.Errorf("%s should not exist without a real run: %q", v, task.Env[v])
		}
	}

	// The params still go: `--param` is how input is passed on that path.
	if task.Env["BREVIS_RUN_PARAMS"] == "" {
		t.Error("the params have to reach the step even with no managed run")
	}
}

func TestTheAttemptStartsAtZeroAsInTheDatabase(t *testing.T) {
	// The task_runs.attempt column has DEFAULT 0, and the pod's name derives
	// from it.
	// Diverging here would make the step report an attempt that does not exist.
	task := runStep(t, app.Runner{RunID: uuid.New(), History: &historico{}})

	if task.Env["BREVIS_RUN_ATTEMPT"] != "0" {
		t.Errorf("first attempt = %q, expected \"0\"", task.Env["BREVIS_RUN_ATTEMPT"])
	}
}

// TestTheDispatchersPath reproduces what cmd/brevis assembles in the
// dispatcher's `executar`: a real run, with an id, params, a trigger and a
// history.
//
// It is the only test covering the way the Runner is actually built in
// production -- the rest of the file tests isolated fields.
func TestTheDispatchersPath(t *testing.T) {
	id := uuid.New()
	when := time.Date(2026, 9, 3, 4, 0, 0, 0, time.UTC)

	task := runStep(t, app.Runner{
		RunID:       id,
		RunAttempt:  0,
		Params:      map[string]string{"load_full": "true"},
		Trigger:     "schedule",
		LogicalDate: &when,
		History:     &historico{jaTeve: false},
		Env:         map[string]string{"PATH": "/usr/bin", "HOME": "/root"},
	})

	// The tasks' environment survives.
	if task.Env["PATH"] == "" || task.Env["HOME"] == "" {
		t.Error("the tasks' configured environment has to keep arriving")
	}

	// E o contexto do run chega junto.
	expected := map[string]string{
		"BREVIS_RUN_ID":           id.String(),
		"BREVIS_RUN_FIRST":        "true",
		"BREVIS_RUN_ATTEMPT":      "0",
		"BREVIS_RUN_TRIGGER":      "schedule",
		"BREVIS_RUN_LOGICAL_DATE": "2026-09-03T04:00:00Z",
		"BREVIS_RUN_PARAMS":       `{"load_full":"true"}`,
	}
	for k, v := range expected {
		if task.Env[k] != v {
			t.Errorf("%s = %q, expected %q", k, task.Env[k], v)
		}
	}
}
