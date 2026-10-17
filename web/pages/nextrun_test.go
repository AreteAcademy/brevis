package pages

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// WHEN IT RUNS NEXT (screen-workflow-v2 WV2), by a fixed clock: "in 12m" with
// the UTC time when the schedule runs, "paused" when it does not, "manual"
// with no schedule. Computed on the server; nothing ticks.
func TestTheNextRunIsSaidByTheClock(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 48, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	cases := []struct {
		why        string
		sum        *postgres.WorkflowSummary
		value, sub string
	}{
		{"active, minutes away", &postgres.WorkflowSummary{HasSchedule: true, Active: true, NextRun: at(12 * time.Minute)}, "in 12m", "Oct 17 04:00 UTC"},
		{"active, hours away", &postgres.WorkflowSummary{HasSchedule: true, Active: true, NextRun: at(3*time.Hour + 20*time.Minute)}, "in 3h 20m", "Oct 17 07:08 UTC"},
		{"active, days away", &postgres.WorkflowSummary{HasSchedule: true, Active: true, NextRun: at(50 * time.Hour)}, "in 2d", "Oct 19 05:48 UTC"},
		{"active, under a minute", &postgres.WorkflowSummary{HasSchedule: true, Active: true, NextRun: at(20 * time.Second)}, "in <1m", "Oct 17 03:48 UTC"},
		{"paused", &postgres.WorkflowSummary{HasSchedule: true, Active: false}, "paused", ""},
		{"no schedule", &postgres.WorkflowSummary{}, "manual", ""},
		{"no summary", nil, "—", ""},
	}
	for _, c := range cases {
		value, sub := NextRunText(c.sum, now)
		if value != c.value || sub != c.sub {
			t.Errorf("%s: got %q %q, want %q %q", c.why, value, sub, c.value, c.sub)
		}
	}
}
