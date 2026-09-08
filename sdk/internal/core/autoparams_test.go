package core

import (
	"os"
	"testing"
	"time"
)

// TestTheAutoParamsSurviveTheTrip.
//
// The JSON tags are the contract between the engine's run.AutoParams and this
// type, and the two live in different modules -- nothing but a test crossing
// the boundary would notice them drifting apart.
//
// The fixture is written by the ENGINE's own test, and the Python library reads
// the same bytes. Three implementations, one file: rename a tag on any side and
// one of the three goes red immediately, instead of every fetcher in the fleet
// quietly reading a window of zero.
func TestTheAutoParamsSurviveTheTrip(t *testing.T) {
	// Outside a repository checkout there is no fixture -- somebody running
	// `go test` on the extracted module. Skipping is honest; failing would
	// report a missing file as a broken contract.
	raw, err := os.ReadFile("../../../lib/python-context/tests/engine_auto_params.json")
	if err != nil {
		t.Skip("no repository checkout, so no engine fixture to check against")
	}
	t.Setenv(EnvAutoParams, string(raw))

	a := AutoParamsFromEnv()
	if got := a.Now().Format(time.RFC3339); got != "2026-09-08T04:00:00Z" {
		t.Errorf("Now() = %s, wanted the slot and not the start", got)
	}
	if a.DelaySeconds != 2220 || !a.PreviousError || a.Date != "2026-09-08" {
		t.Errorf("%+v did not survive the trip", a)
	}
	start, end, ok := a.Window()
	if !ok {
		t.Fatal("there is no window")
	}
	if start.Format(time.RFC3339) != "2026-09-07T04:00:00Z" || !end.Equal(a.AdjustedAt) {
		t.Errorf("window = [%s, %s)", start, end)
	}
	if a.ScheduledAt == nil || a.StartedAt == nil || a.PreviousSuccessAt == nil {
		t.Errorf("an optional timestamp was dropped: %+v", a)
	}
}

// Outside the engine there is nothing to read, and a fetcher run by hand has
// to behave exactly as it did before this type existed: Now() is the wall
// clock, and there is no window.
func TestOutsideTheEngineNowIsTheWallClock(t *testing.T) {
	if v, ok := os.LookupEnv(EnvAutoParams); ok {
		t.Fatalf("the test environment already carries %s=%q", EnvAutoParams, v)
	}
	a := AutoParamsFromEnv()
	if _, _, ok := a.Window(); ok {
		t.Error("a fetcher run by hand was given a window")
	}
	if d := time.Since(a.Now()); d < 0 || d > time.Minute {
		t.Errorf("Now() = %s, which is not the wall clock", a.Now())
	}
	if a.Args() != nil {
		t.Errorf("Args() = %v outside the engine", a.Args())
	}
}

// A malformed value loses the params, never the run: whatever wrote that
// variable is broken, and failing a load over it would turn a cosmetic bug
// into an incident.
func TestMalformedAutoParamsDoNotBreakTheRun(t *testing.T) {
	t.Setenv(EnvAutoParams, "{not json")
	a := AutoParamsFromEnv()
	if !a.AdjustedAt.IsZero() {
		t.Errorf("AdjustedAt = %s from a malformed value", a.AdjustedAt)
	}
	if a.Now().IsZero() {
		t.Error("Now() is zero, so a fetcher would ask for everything since year 1")
	}
}

// The run context carries them, which is the path a Pipeline actually takes:
// nobody calls AutoParamsFromEnv by hand.
func TestTheRunContextCarriesTheAutoParams(t *testing.T) {
	t.Setenv(EnvRunID, "run-1")
	t.Setenv(EnvAutoParams, `{"adjusted_at":"2026-09-08T04:00:00Z","date":"2026-09-08","delay_seconds":60}`)

	rc := RunContextFromEnv()
	if rc.Auto.Date != "2026-09-08" {
		t.Errorf("the run context lost the auto params: %+v", rc.Auto)
	}
	// And they reach the log, because "which window did this run ask for" is a
	// question asked after the fact, from the log, not during.
	var found bool
	for _, a := range rc.Args() {
		if s, ok := a.(string); ok && s == "adjusted_at" {
			found = true
		}
	}
	if !found {
		t.Errorf("Args() = %v, without the clock", rc.Args())
	}
}
