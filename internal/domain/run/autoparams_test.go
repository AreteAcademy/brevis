package run

import (
	"encoding/json"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestALateRunReadsTheSlotAndNotTheWallClock is the whole point.
//
// A fetcher that reads now() and subtracts its window is right on a run that
// starts on time and forty minutes wrong on one the queue delayed -- and those
// forty minutes belong to no run at all, because the next slot reads its own
// now() too. Nothing fails; the data is simply missing.
func TestALateRunReadsTheSlotAndNotTheWallClock(t *testing.T) {
	slot := at("2026-09-08T04:00:00Z")
	started := at("2026-09-08T04:37:00Z")

	a := Auto(Run{LogicalDate: &slot}, started, Previous{}, Interval{})

	if !a.AdjustedAt.Equal(slot) {
		t.Errorf("adjusted_at = %s; a late run has to read the slot", a.AdjustedAt)
	}
	if a.DelaySeconds != 37*60 {
		t.Errorf("delay = %ds, wanted %d", a.DelaySeconds, 37*60)
	}
}

// And a RETRY does not move it. A run that fails at 04:05 and is retried at
// 07:20 has to read the same window as the attempt that failed, or the retry
// fetches different data than the run it is retrying.
func TestARetryReadsTheSameClock(t *testing.T) {
	slot := at("2026-09-08T04:00:00Z")

	first := Auto(Run{LogicalDate: &slot}, at("2026-09-08T04:05:00Z"), Previous{}, Interval{})
	retry := Auto(Run{LogicalDate: &slot}, at("2026-09-08T07:20:00Z"), Previous{}, Interval{})

	if !first.AdjustedAt.Equal(retry.AdjustedAt) {
		t.Errorf("the retry reads %s and the first attempt read %s",
			retry.AdjustedAt, first.AdjustedAt)
	}
	// The delay does move, and it should: it describes THIS attempt.
	if retry.DelaySeconds <= first.DelaySeconds {
		t.Errorf("the retry's delay (%d) is not later than the first's (%d)",
			retry.DelaySeconds, first.DelaySeconds)
	}
}

// A manual run belongs to no slot, so the only honest clock is when it started.
func TestAManualRunReadsWhenItStarted(t *testing.T) {
	started := at("2026-09-08T11:03:00Z")
	a := Auto(Run{TriggerType: "manual"}, started, Previous{}, Interval{})

	if !a.AdjustedAt.Equal(started) {
		t.Errorf("adjusted_at = %s, wanted the start", a.AdjustedAt)
	}
	if a.ScheduledAt != nil {
		t.Errorf("a manual run got a slot: %s", a.ScheduledAt)
	}
	if a.DelaySeconds != 0 {
		t.Errorf("a manual run is %ds late, which is not a thing", a.DelaySeconds)
	}
}

// A run that started EARLY -- clock skew between the scheduler and the
// database -- is not negatively late. A negative delay in a dashboard is a
// number somebody has to explain.
func TestDelayIsNeverNegative(t *testing.T) {
	slot := at("2026-09-08T04:00:00Z")
	a := Auto(Run{LogicalDate: &slot}, at("2026-09-08T03:59:58Z"), Previous{}, Interval{})
	if a.DelaySeconds != 0 {
		t.Errorf("delay = %d", a.DelaySeconds)
	}
}

func TestTheWindowTravelsThrough(t *testing.T) {
	slot := at("2026-09-08T04:00:00Z")
	a := Auto(Run{LogicalDate: &slot}, slot, Previous{},
		Interval{Start: at("2026-09-07T04:00:00Z"), End: slot})

	if a.IntervalStart == nil || !a.IntervalStart.Equal(at("2026-09-07T04:00:00Z")) {
		t.Errorf("interval_start = %v", a.IntervalStart)
	}
	// End is the slot, EXCLUDED: what arrives after it belongs to the next run.
	if a.IntervalEnd == nil || !a.IntervalEnd.Equal(slot) {
		t.Errorf("interval_end = %v", a.IntervalEnd)
	}
}

// A workflow with no schedule has no window, and an INVENTED one would hand a
// pipeline an arbitrary slice of history.
func TestNoScheduleMeansNoWindow(t *testing.T) {
	a := Auto(Run{}, at("2026-09-08T11:00:00Z"), Previous{}, Interval{})
	if a.IntervalStart != nil || a.IntervalEnd != nil {
		t.Errorf("a workflow with no schedule got a window: %v..%v", a.IntervalStart, a.IntervalEnd)
	}
}

// previous_error says something is wrong; previous_success_at says how far back
// to catch up. The boolean on its own is not actionable.
func TestThePreviousRunIsReportedWithSomethingToActOn(t *testing.T) {
	slot := at("2026-09-08T04:00:00Z")
	success := at("2026-09-06T04:00:00Z")

	a := Auto(Run{LogicalDate: &slot}, slot,
		Previous{Failed: true, SuccessAt: &success}, Interval{})

	if !a.PreviousError {
		t.Error("previous_error is false after a failure")
	}
	if a.PreviousSuccessAt == nil || !a.PreviousSuccessAt.Equal(success) {
		t.Errorf("previous_success_at = %v; a bool alone does not say how far back to go",
			a.PreviousSuccessAt)
	}
}

// TestTheOptionalVariablesAreAbsentAndNotEmpty.
//
// A variable that is always there and sometimes blank makes every reader write
// the same two-line check. An absent one makes `${X:-default}` work, which is
// the whole vocabulary a shell step has.
func TestTheOptionalVariablesAreAbsentAndNotEmpty(t *testing.T) {
	env := Auto(Run{}, at("2026-09-08T11:00:00Z"), Previous{}, Interval{}).Env()

	for _, absent := range []string{
		"BREVIS_AUTO_SCHEDULED_AT", "BREVIS_AUTO_INTERVAL_START",
		"BREVIS_AUTO_INTERVAL_END", "BREVIS_AUTO_PREVIOUS_SUCCESS_AT",
	} {
		if v, there := env[absent]; there {
			t.Errorf("%s is set to %q on a run that has none", absent, v)
		}
	}
	// The ones that always have an answer are always there.
	for _, always := range []string{
		"BREVIS_AUTO_ADJUSTED_AT", "BREVIS_AUTO_DATE",
		"BREVIS_AUTO_DELAY_SECONDS", "BREVIS_AUTO_PREVIOUS_ERROR",
	} {
		if env[always] == "" {
			t.Errorf("%s is missing", always)
		}
	}
}

// A bash step with no library reads one variable; a step that would rather
// parse one value reads the JSON. Both, because requiring `jq` to answer "what
// time is it for this run" puts a dependency in front of the simplest step
// there is.
func TestBothShapesAreOffered(t *testing.T) {
	slot := at("2026-09-08T04:00:00Z")
	a := Auto(Run{LogicalDate: &slot}, at("2026-09-08T04:37:00Z"), Previous{}, Interval{})
	env := a.Env()

	if env["BREVIS_AUTO_ADJUSTED_AT"] != "2026-09-08T04:00:00Z" {
		t.Errorf("adjusted_at = %q", env["BREVIS_AUTO_ADJUSTED_AT"])
	}
	var back AutoParams
	if err := json.Unmarshal([]byte(env["BREVIS_AUTO_PARAMS"]), &back); err != nil {
		t.Fatalf("the json blob does not parse: %v", err)
	}
	if !back.AdjustedAt.Equal(a.AdjustedAt) || back.DelaySeconds != a.DelaySeconds {
		t.Errorf("the two shapes disagree: %+v vs %+v", back, a)
	}
}

// The date is UTC and comes from the CLOCK, not from the wall. A late run's
// partition must not land on the next day because it started at 00:12.
func TestTheDateFollowsTheAdjustedClock(t *testing.T) {
	slot := at("2026-09-07T23:00:00Z")
	a := Auto(Run{LogicalDate: &slot}, at("2026-09-08T00:12:00Z"), Previous{}, Interval{})
	if a.Date != "2026-09-07" {
		t.Errorf("date = %q; a late run wrote into the next day's partition", a.Date)
	}
}
