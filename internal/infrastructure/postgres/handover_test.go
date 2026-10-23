package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func handoverOf(t *testing.T, pool *postgres.Pool, runID uuid.UUID) *execution.Handover {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT handover FROM task_runs WHERE run_id = $1 AND node_id = 'long'`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		return nil
	}
	var h execution.Handover
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	return &h
}

// #125: the handover lives on the step's row, which stays running, and lasts
// exactly as long as the attempt is handed over -- starting the attempt again
// or ending it clears it, so the next scheduler never follows a stale one.
func TestAHandoverIsKeptOnTheRowUntilTheAttemptMovesOn(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "daily", IdempotencyKey: uuid.NewString(), Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	step := dom.Step("long")
	if err := repo.IniciarTask(ctx, r.ID, step, 0); err != nil {
		t.Fatal(err)
	}

	want := execution.Handover{Instance: "http://agent-0:9443", ExecutionID: "daily:long:x:r0:a0", After: 41}
	if err := repo.RecordHandover(ctx, r.ID, step, 0, want); err != nil {
		t.Fatal(err)
	}
	if got := handoverOf(t, pool, r.ID); got == nil || *got != want {
		t.Fatalf("handover = %+v; wanted %+v", got, want)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM task_runs WHERE run_id = $1`, r.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(dom.StatusRunning) {
		t.Errorf("a handed-over row reads %s; it is running", status)
	}

	if err := repo.IniciarTask(ctx, r.ID, step, 0); err != nil {
		t.Fatal(err)
	}
	if got := handoverOf(t, pool, r.ID); got != nil {
		t.Errorf("starting the attempt again kept the handover %+v", got)
	}

	if err := repo.RecordHandover(ctx, r.ID, step, 0, want); err != nil {
		t.Fatal(err)
	}
	if err := repo.TerminarTask(ctx, r.ID, step, 0, dom.StatusSuccess, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := handoverOf(t, pool, r.ID); got != nil {
		t.Errorf("ending the attempt kept the handover %+v", got)
	}
}

// Written nowhere is not written: with no row for the attempt, the runner has
// to be told, so it stops the step instead of leaving it where nobody looks.
func TestAHandoverWithNoRowIsAnError(t *testing.T) {
	pool := insightsDB(t)
	err := postgres.NewRunRepo(pool).RecordHandover(context.Background(), uuid.New(), dom.Step("long"), 0,
		execution.Handover{ExecutionID: "x"})
	if err == nil {
		t.Error("a handover recorded on no row reported success")
	}
}

// #125: what a stopped scheduler left running in a run, read once by the
// next. Only rows still running with a handover; never another run's.
func TestARunsHandoversAreWhatItLeftRunning(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	newRun := func() uuid.UUID {
		r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "daily", IdempotencyKey: uuid.NewString(), Definition: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	mine, other := newRun(), newRun()

	handed := dom.StepKey{Node: "load", MapIndex: 3}
	finished := dom.Step("extract")
	h := execution.Handover{Instance: "http://agent-0:9443", ExecutionID: "daily:load-3:x:r0:a1", Attempt: 1, After: 9}

	// The other run's handover is on a step of its own, so leaking it into
	// this run's answer cannot hide behind a key this run also has.
	theirs := execution.Handover{ExecutionID: "daily:transform:y:r0:a0"}
	for _, s := range []struct {
		run     uuid.UUID
		step    dom.StepKey
		attempt int
		h       execution.Handover
	}{{mine, handed, 1, h}, {mine, finished, 0, h}, {other, dom.Step("transform"), 0, theirs}} {
		if err := repo.IniciarTask(ctx, s.run, s.step, s.attempt); err != nil {
			t.Fatal(err)
		}
		if err := repo.RecordHandover(ctx, s.run, s.step, s.attempt, s.h); err != nil {
			t.Fatal(err)
		}
	}
	// One of mine ended after its handover: its reattach finished it.
	if err := repo.TerminarTask(ctx, mine, finished, 0, dom.StatusSuccess, nil, "", ""); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Handovers(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[handed] != h {
		t.Errorf("handovers = %+v; wanted only %v -> %+v", got, handed, h)
	}
}
