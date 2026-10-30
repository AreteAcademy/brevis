package pages

import (
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// THE LAST SLOTS OF A SCHEDULE, newest first, up to now: each with the window
// it processes, from the slot before it to itself -- the same window its
// interval_start and interval_end will be.
func TestRecentSlotsAreTheScheduleBackwards(t *testing.T) {
	got := RecentSlots("0 9 * * *", "UTC", utc("2026-10-26T10:00:00Z"), 3)
	want := []Slot{
		{At: utc("2026-10-26T09:00:00Z"), From: utc("2026-10-25T09:00:00Z")},
		{At: utc("2026-10-25T09:00:00Z"), From: utc("2026-10-24T09:00:00Z")},
		{At: utc("2026-10-24T09:00:00Z"), From: utc("2026-10-23T09:00:00Z")},
	}
	if len(got) != len(want) {
		t.Fatalf("slots = %v", got)
	}
	for i := range want {
		if !got[i].At.Equal(want[i].At) || !got[i].From.Equal(want[i].From) {
			t.Errorf("slot %d = %s from %s, want %s from %s", i, got[i].At, got[i].From, want[i].At, want[i].From)
		}
	}
}

// A SLOT AT THIS VERY INSTANT IS PAST: it fired.
func TestRecentSlotsIncludeNow(t *testing.T) {
	got := RecentSlots("*/15 * * * *", "", utc("2026-10-26T10:15:00Z"), 2)
	if len(got) != 2 || !got[0].At.Equal(utc("2026-10-26T10:15:00Z")) || !got[1].At.Equal(utc("2026-10-26T10:00:00Z")) {
		t.Errorf("slots = %v", got)
	}
	if !got[0].From.Equal(utc("2026-10-26T10:00:00Z")) {
		t.Errorf("a quarter-hour slot's window starts %s", got[0].From)
	}
}

// THE CRON IS READ IN ITS ZONE: 04:00 in São Paulo is 07:00 UTC.
func TestRecentSlotsReadTheCronInItsZone(t *testing.T) {
	got := RecentSlots("0 4 * * *", "America/Sao_Paulo", utc("2026-10-28T12:00:00Z"), 1)
	if len(got) != 1 || !got[0].At.Equal(utc("2026-10-28T07:00:00Z")) {
		t.Errorf("slots = %v", got)
	}
}

// A SCHEDULE THAT NEVER FIRES, or one that cannot be read, has no slots.
func TestRecentSlotsOfNothing(t *testing.T) {
	for _, cron := range []string{"0 0 30 2 *", "not a cron", ""} {
		if got := RecentSlots(cron, "UTC", utc("2026-10-28T12:00:00Z"), 14); len(got) != 0 {
			t.Errorf("%q has slots %v", cron, got)
		}
	}
}
