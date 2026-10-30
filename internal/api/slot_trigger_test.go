package api_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// zoneReads is a catalog whose schedules say their zone.
type zoneReads struct {
	api.Leitura
	schedules []postgres.ScheduleSummary
}

func (z zoneReads) Schedules(context.Context) ([]postgres.ScheduleSummary, error) {
	return z.schedules, nil
}

func slotUI(spy *triggerSpy, zone string) *api.UI {
	reads := zoneReads{schedules: []postgres.ScheduleSummary{
		{WorkflowSlug: "daily", Cron: "0 4 * * *", Timezone: zone, Active: true},
	}}
	return api.NewUI(reads, nil, nil, spy, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
}

// THE SLOT REACHES THE SCHEDULER AS AN INSTANT, typed either way: RFC3339 says
// its own offset, and the browser's datetime-local is read in the schedule's
// zone -- 04:00 in São Paulo is 07:00 UTC.
func TestATriggersSlotIsReadAsAnInstant(t *testing.T) {
	for _, c := range []struct {
		name, zone, slot string
		want             time.Time
	}{
		{"RFC3339 in UTC", "UTC", "2026-10-27T04:00:00Z", time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)},
		{"RFC3339 with an offset", "UTC", "2026-10-27T01:00:00-03:00", time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)},
		{"datetime-local, UTC schedule", "UTC", "2026-10-27T04:00", time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)},
		{"datetime-local with seconds", "", "2026-10-27T04:00:00", time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)},
		{"datetime-local, São Paulo schedule", "America/Sao_Paulo", "2026-10-27T04:00", time.Date(2026, 10, 27, 7, 0, 0, 0, time.UTC)},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := &triggerSpy{}
			rec := postTrigger(t, slotUI(spy, c.zone), "daily", url.Values{"slot": {c.slot}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			if spy.slot == nil || !spy.slot.Equal(c.want) {
				t.Errorf("slot = %v, want %s", spy.slot, c.want)
			}
		})
	}
}

// A SLOT THAT IS NOT A TIME IS THE OPERATOR'S MISTAKE: 400, saying what was
// typed, and nothing is triggered.
func TestATriggerRefusesASlotThatIsNotATime(t *testing.T) {
	spy := &triggerSpy{}
	rec := postTrigger(t, slotUI(spy, "UTC"), "daily", url.Values{"slot": {"yesterday"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"yesterday"`) {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if spy.called {
		t.Error("an unreadable slot was triggered")
	}
}

// THE SCHEDULER'S REFUSAL REACHES THE OPERATOR as it was written: the nearest
// slot is the answer they need.
func TestATriggerSaysWhyTheSlotWasRefused(t *testing.T) {
	spy := &triggerSpy{err: errors.New(`2026-10-28 05:30 UTC is not a slot of "0 4 * * *"; the nearest before it is 2026-10-28 04:00 UTC`)}
	rec := postTrigger(t, slotUI(spy, "UTC"), "daily", url.Values{"slot": {"2026-10-28T05:30:00Z"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "the nearest before it is 2026-10-28 04:00 UTC") {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

// NO SLOT, NO CHANGE: a click is still a run of no slot, and an empty field
// is no slot.
func TestATriggerWithNoSlotSendsNone(t *testing.T) {
	for _, form := range []url.Values{{}, {"slot": {""}}} {
		spy := &triggerSpy{}
		rec := postTrigger(t, slotUI(spy, "UTC"), "daily", form)
		if rec.Code != http.StatusSeeOther || spy.slot != nil {
			t.Errorf("form %v: status = %d, slot = %v", form, rec.Code, spy.slot)
		}
	}
}
