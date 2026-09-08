package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wfdom "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// scheduled creates a run for a slot, with a schedule on its definition.
func scheduled(t *testing.T, repo *postgres.RunRepo, slug, cron string, slot time.Time) uuid.UUID {
	t.Helper()
	def, err := json.Marshal(wfdom.Workflow{Slug: slug, Schedule: cron})
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.Create(context.Background(), dom.Run{
		WorkflowSlug: slug, IdempotencyKey: uuid.NewString(),
		TriggerType: "schedule", Definition: def, LogicalDate: &slot,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// TestTheRunCarriesTheClockItShouldRead is the feature, through the real path:
// the dispatcher works the params out when the run starts and the runner reads
// them off the Run.
func TestTheRunCarriesTheClockItShouldRead(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	slot := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	id := scheduled(t, repo, "nightly", "0 4 * * *", slot)
	_ = repo.Transicionar(ctx, id, dom.StatusQueued)
	_ = q.Enqueue(ctx, id, 0, time.Time{})

	var seen dom.AutoParams
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, Interval: 10 * time.Millisecond,
	}, q, repo, func(ctx context.Context, runID uuid.UUID) error {
		// What the runner sees, which is the only thing that matters.
		r, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		seen = r.Auto
		return nil
	}, noLog())

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool {
		current, _ := repo.Get(context.Background(), id)
		return current.Status == dom.StatusSuccess
	})
	cancel()
	time.Sleep(100 * time.Millisecond)

	// The clock is the SLOT, not the wall: this run started long after 04:00
	// on a fixture date in the past.
	if !seen.AdjustedAt.Equal(slot) {
		t.Errorf("adjusted_at = %s, wanted the slot %s", seen.AdjustedAt, slot)
	}
	if seen.Date != "2026-09-08" {
		t.Errorf("date = %q", seen.Date)
	}
	// The window comes from the run's OWN definition's cron.
	if seen.IntervalStart == nil || !seen.IntervalStart.Equal(slot.AddDate(0, 0, -1)) {
		t.Errorf("interval_start = %v, wanted the previous slot", seen.IntervalStart)
	}
	if seen.IntervalEnd == nil || !seen.IntervalEnd.Equal(slot) {
		t.Errorf("interval_end = %v", seen.IntervalEnd)
	}
	// It ran now, so it is very late against a slot in the past -- and the
	// number is there so nobody subtracts two timestamps.
	if seen.DelaySeconds <= 0 {
		t.Errorf("delay = %d on a run of a slot in the past", seen.DelaySeconds)
	}
	// And it is stored, not only handed over.
	stored, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Auto.AdjustedAt.Equal(seen.AdjustedAt) {
		t.Errorf("what was stored differs from what the run read")
	}
}

// TestPreviousErrorLooksAtTheRunBefore, and the run before is the one with the
// closest earlier slot -- not the most recently created, which a manual run in
// the middle of a nightly would be.
func TestPreviousErrorLooksAtTheRunBefore(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	yesterday := time.Date(2026, 9, 7, 4, 0, 0, 0, time.UTC)
	old := scheduled(t, repo, "nightly", "0 4 * * *", yesterday)
	_ = repo.Transicionar(ctx, old, dom.StatusQueued)
	_ = repo.Transicionar(ctx, old, dom.StatusRunning)
	_ = repo.Transicionar(ctx, old, dom.StatusFailed)

	today := scheduled(t, repo, "nightly", "0 4 * * *", yesterday.AddDate(0, 0, 1))
	_ = repo.Transicionar(ctx, today, dom.StatusQueued)
	_ = repo.Transicionar(ctx, today, dom.StatusRunning)

	auto, err := repo.RecordAuto(ctx, today, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !auto.PreviousError {
		t.Error("previous_error is false, and yesterday's run failed")
	}
	if auto.PreviousSuccessAt != nil {
		t.Errorf("previous_success_at = %v, and nothing has ever succeeded", auto.PreviousSuccessAt)
	}

	// Now make yesterday a success and check the other side. A rule that always
	// says true passes the half above.
	_ = repo.Transicionar(ctx, old, dom.StatusRetrying)
	_ = repo.Transicionar(ctx, old, dom.StatusQueued)
	_ = repo.Transicionar(ctx, old, dom.StatusRunning)
	_ = repo.Transicionar(ctx, old, dom.StatusSuccess)

	auto, err = repo.RecordAuto(ctx, today, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auto.PreviousError {
		t.Error("previous_error is true after the run before it succeeded")
	}
	if auto.PreviousSuccessAt == nil || !auto.PreviousSuccessAt.Equal(yesterday) {
		t.Errorf("previous_success_at = %v, wanted %s", auto.PreviousSuccessAt, yesterday)
	}
}

// A workflow's FIRST run has no previous run, and that is not an error: a
// boolean that cannot distinguish "the last one failed" from "there was no last
// one" would have every pipeline widening its window on day one.
func TestAFirstRunHasNoPreviousError(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	slot := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	id := scheduled(t, repo, "brand_new", "0 4 * * *", slot)
	_ = repo.Transicionar(ctx, id, dom.StatusQueued)
	_ = repo.Transicionar(ctx, id, dom.StatusRunning)

	auto, err := repo.RecordAuto(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auto.PreviousError {
		t.Error("a workflow's first run reports that the previous one failed")
	}
}

// The params failing to be recorded does not fail the run. A run without them
// still runs, and its steps fall back to their own clock -- which is what every
// pipeline did before this existed.
func TestARunSurvivesTheParamsNotBeingRecorded(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	slot := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	// A definition whose cron cannot be parsed: the window comes back empty
	// and everything else still has to work.
	def, _ := json.Marshal(wfdom.Workflow{Slug: "broken_cron", Schedule: "not a cron"})
	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "broken_cron", IdempotencyKey: uuid.NewString(),
		TriggerType: "schedule", Definition: def, LogicalDate: &slot,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = q.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, Interval: 10 * time.Millisecond,
	}, q, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool {
		current, _ := repo.Get(context.Background(), r.ID)
		return current.Status == dom.StatusSuccess
	})
	cancel()

	stored, err := repo.Get(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	// No window, and the clock is still there.
	if stored.Auto.IntervalStart != nil {
		t.Errorf("a broken cron produced a window: %v", stored.Auto.IntervalStart)
	}
	if !stored.Auto.AdjustedAt.Equal(slot) {
		t.Errorf("adjusted_at = %s", stored.Auto.AdjustedAt)
	}
}
