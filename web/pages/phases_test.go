package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/run"
)

var created = time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := created.Add(d); return &t }

// states is the strip as "key:state" pairs, the shape every case asserts.
func states(ps []Phase) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Key+":"+p.State)
	}
	return strings.Join(out, " ")
}

// A RUN'S PHASES COME FROM ITS OWN TIMES, AND ITS STATUS SAYS WHICH IS NOW.
// One case per state the strip draws differently.
func TestPhasesFollowTheRunsTimes(t *testing.T) {
	now := created.Add(10 * time.Minute)
	for _, c := range []struct {
		name    string
		r       run.Run
		want    string
		details map[string]string
	}{
		{"queued", run.Run{Status: run.StatusQueued, CreatedAt: created},
			"scheduled:done started:current running:pending finished:pending",
			map[string]string{"started": "waiting 10m 00s"}},
		{"running", run.Run{Status: run.StatusRunning, CreatedAt: created, StartedAt: at(2 * time.Second)},
			"scheduled:done started:done running:current finished:pending",
			map[string]string{"started": "+2.0s", "running": "9m 58s"}},
		{"succeeded", run.Run{Status: run.StatusSuccess, CreatedAt: created, StartedAt: at(2 * time.Second), FinishedAt: at(92 * time.Second)},
			"scheduled:done started:done running:done finished:done",
			map[string]string{"running": "1m 30s", "finished": "succeeded"}},
		{"failed", run.Run{Status: run.StatusFailed, CreatedAt: created, StartedAt: at(time.Second), FinishedAt: at(31 * time.Second)},
			"scheduled:done started:done running:done finished:failed",
			map[string]string{"running": "30.0s", "finished": "failed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ps := PhasesOf(c.r, now)
			if got := states(ps); got != c.want {
				t.Fatalf("phases = %s\nwant     %s", got, c.want)
			}
			for _, p := range ps {
				if want, ok := c.details[p.Key]; ok && !strings.Contains(p.Detail, want) {
					t.Errorf("%s says %q, want %q in it", p.Key, p.Detail, want)
				}
			}
		})
	}
}

// A SCHEDULED RUN NAMES ITS SLOT; one triggered by hand says so instead.
func TestTheFirstPhaseSaysHowTheRunWasMade(t *testing.T) {
	slot := created
	ps := PhasesOf(run.Run{Status: run.StatusQueued, CreatedAt: created, LogicalDate: &slot, TriggerType: "schedule"}, created)
	if ps[0].Label != "Scheduled" || !strings.Contains(ps[0].Detail, "2026-10-15 04:00") {
		t.Errorf("a scheduled run's first phase is %+v", ps[0])
	}
	// How late it started is counted from the slot, as its `delay` auto param
	// is: created 1s after the slot and started 3s after it is "+3.0s".
	ps = PhasesOf(run.Run{Status: run.StatusSuccess, CreatedAt: created.Add(time.Second), LogicalDate: &slot,
		StartedAt: at(3 * time.Second), FinishedAt: at(4 * time.Second)}, created)
	if ps[1].Detail != "+3.0s" {
		t.Errorf("a scheduled run started %q after its slot, want +3.0s", ps[1].Detail)
	}
	ps = PhasesOf(run.Run{Status: run.StatusQueued, CreatedAt: created, TriggerType: "manual"}, created)
	if ps[0].Label != "Triggered" {
		t.Errorf("a manual run's first phase is %+v", ps[0])
	}
}

// THE STRIP IS IN THE RUN'S LIVE REGION, so it refreshes with the rest of
// the head while the run moves.
func TestThePhasesAreDrawnInTheLiveHead(t *testing.T) {
	r := run.Run{Status: run.StatusRunning, CreatedAt: created, StartedAt: at(time.Second), WorkflowSlug: "daily"}
	page := draw(t, RunLive(r, nil, nil, nil))
	head := page[:strings.Index(page, `data-live-target="run-body"`)]
	if !strings.Contains(head, "data-phases") || !strings.Contains(head, `data-phase-state="current"`) {
		t.Error("the live head does not draw the phases")
	}
}
