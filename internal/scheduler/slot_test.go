package scheduler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

func at(t time.Time) *time.Time { return &t }

// A PAST SLOT, RUN BY HAND: the run is manual, its logical date is the slot,
// and its automatic params are that slot's -- the same run the schedule made
// that night, without a backfill.
func TestATriggerForAPastSlotRunsThatSlot(t *testing.T) {
	s, runs, _, _ := build(t, "0 4 * * *", false)
	ctx := context.Background()
	slot := inUTC("2026-10-27T04:00:00Z")
	now := inUTC("2026-10-29T15:20:07Z")

	id, err := s.Disparar(ctx, "diario", now, nil, at(slot))
	if err != nil {
		t.Fatal(err)
	}
	r, err := runs.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.TriggerType != "manual" {
		t.Errorf("trigger = %q, want manual", r.TriggerType)
	}
	if r.LogicalDate == nil || !r.LogicalDate.Equal(slot) {
		t.Errorf("logical date = %v, want the slot %s", r.LogicalDate, slot)
	}
	if want := "diario:manual:2026-10-27T04:00:00Z:2026-10-29T15:20:07Z"; r.IdempotencyKey != want {
		t.Errorf("key = %q, want %q", r.IdempotencyKey, want)
	}

	if err := runs.Transicionar(ctx, id, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}
	auto, err := runs.RecordAuto(ctx, id, func(slot time.Time) (time.Time, time.Time) {
		return slot.Add(-24 * time.Hour), slot
	})
	if err != nil {
		t.Fatal(err)
	}
	if !auto.AdjustedAt.Equal(slot) || auto.Date != "2026-10-27" {
		t.Errorf("auto params = %s / %s, want the slot's", auto.AdjustedAt, auto.Date)
	}
}

// THE SAME SLOT CAN BE RUN AGAIN, and two clicks in one second are still one.
func TestTheSameSlotRunsAgainButNotTwiceInASecond(t *testing.T) {
	s, _, _, _ := build(t, "0 4 * * *", false)
	ctx := context.Background()
	slot := inUTC("2026-10-27T04:00:00Z")

	first, err := s.Disparar(ctx, "diario", inUTC("2026-10-29T15:20:07Z"), nil, at(slot))
	if err != nil || first == uuid.Nil {
		t.Fatalf("first = %v, %v", first, err)
	}
	again, err := s.Disparar(ctx, "diario", inUTC("2026-10-29T15:20:07.600Z"), nil, at(slot))
	if err != nil || again != uuid.Nil {
		t.Errorf("a second click in the same second = %v, %v, want nothing", again, err)
	}
	later, err := s.Disparar(ctx, "diario", inUTC("2026-10-29T15:31:00Z"), nil, at(slot))
	if err != nil || later == uuid.Nil || later == first {
		t.Errorf("the same slot later = %v, %v, want a new run", later, err)
	}
}

// A SLOT THE SCHEDULE NEVER FIRES IS REFUSED, and the refusal says the slot
// that would be right: the nearest one before it.
func TestASlotOffTheCronIsRefusedWithTheNearest(t *testing.T) {
	s, _, _, _ := build(t, "0 4 * * *", false)
	_, err := s.Disparar(context.Background(), "diario", inUTC("2026-10-29T15:00:00Z"), nil,
		at(inUTC("2026-10-28T05:30:00Z")))
	if err == nil {
		t.Fatal("a slot off the cron was accepted")
	}
	for _, want := range []string{"0 4 * * *", "2026-10-28 04:00 UTC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

func TestASlotInTheFutureIsRefused(t *testing.T) {
	s, _, _, _ := build(t, "0 4 * * *", false)
	_, err := s.Disparar(context.Background(), "diario", inUTC("2026-10-29T15:00:00Z"), nil,
		at(inUTC("2026-10-30T04:00:00Z")))
	if err == nil || !strings.Contains(err.Error(), "future") {
		t.Errorf("a future slot: %v", err)
	}
}

func TestASlotOfAWorkflowWithNoScheduleIsRefused(t *testing.T) {
	s, _, _, _ := build(t, "", false)
	_, err := s.Disparar(context.Background(), "diario", inUTC("2026-10-29T15:00:00Z"), nil,
		at(inUTC("2026-10-28T04:00:00Z")))
	if err == nil || !strings.Contains(err.Error(), "no schedule") {
		t.Errorf("a slot with no schedule: %v", err)
	}
}

// A PAUSED SCHEDULE STILL HAS ITS SLOTS: pausing stops the next ones, and
// rerunning a past one is exactly what somebody does while it is paused.
func TestAPausedScheduleStillTakesAPastSlot(t *testing.T) {
	s, _, _, pool := build(t, "0 4 * * *", false)
	if _, err := pool.Exec(context.Background(),
		`UPDATE schedules SET ativo = false WHERE workflow_slug = 'diario'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Disparar(context.Background(), "diario", inUTC("2026-10-29T15:00:00Z"), nil,
		at(inUTC("2026-10-28T04:00:00Z"))); err != nil {
		t.Errorf("a past slot of a paused schedule: %v", err)
	}
}

// THE CRON IS READ IN THE SCHEDULE'S ZONE: 04:00 in São Paulo is 07:00 UTC,
// and the refusal says the nearest slot both ways.
func TestASlotIsReadInTheSchedulesZone(t *testing.T) {
	s, _, _, pool := build(t, "0 4 * * *", false)
	if _, err := pool.Exec(context.Background(),
		`UPDATE schedules SET timezone = 'America/Sao_Paulo' WHERE workflow_slug = 'diario'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := inUTC("2026-10-29T15:00:00Z")
	if _, err := s.Disparar(ctx, "diario", now, nil, at(inUTC("2026-10-28T07:00:00Z"))); err != nil {
		t.Errorf("04:00 in São Paulo: %v", err)
	}
	_, err := s.Disparar(ctx, "diario", now, nil, at(inUTC("2026-10-28T04:00:00Z")))
	if err == nil {
		t.Fatal("04:00 UTC was accepted for a São Paulo schedule")
	}
	for _, want := range []string{"2026-10-27 07:00 UTC", "2026-10-27 04:00 America/Sao_Paulo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

// WITH NO SLOT NOTHING CHANGES: a click is a run of no slot.
func TestATriggerWithNoSlotHasNoLogicalDate(t *testing.T) {
	s, runs, _, _ := build(t, "0 4 * * *", false)
	ctx := context.Background()
	id, err := s.Disparar(ctx, "diario", inUTC("2026-10-29T15:20:07Z"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runs.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.LogicalDate != nil || r.IdempotencyKey != "diario:manual:2026-10-29T15:20:07Z" {
		t.Errorf("logical date = %v, key = %q", r.LogicalDate, r.IdempotencyKey)
	}
}
