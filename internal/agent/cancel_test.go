package agent_test

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
)

// What `ps` reports is whole seconds -- and on Linux it is the boot time plus
// ticks, the boot time itself a whole second that can be off by one. The
// recorded instant is taken just before the fork. So the same process can be
// reported up to a second either side of the second it was recorded in, and a
// cancel must still recognise it: refusing it leaves a step running that the
// engine asked to stop. A recycled pid started seconds or days later, and is
// still refused.
func TestTheStartComparisonToleratesWholeSecondsAndRefusesARecycledPid(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("15:04:05.000", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name               string
		recorded, reported string
		same               bool
	}{
		{"reported as the second it started in", "12:00:00.950", "12:00:00.000", true},
		{"the fork crossed into the next second", "12:00:00.990", "12:00:01.000", true},
		{"the boot time rounded a second down", "12:00:00.990", "11:59:59.000", true},
		{"the boot time rounded a second up, across a second", "12:00:00.990", "12:00:02.000", true},
		{"a pid recycled ten seconds later", "12:00:00.500", "12:00:10.000", false},
		{"a pid reported before it could have started", "12:00:00.500", "11:59:55.000", false},
	}
	for _, c := range cases {
		if got := agent.StartMatches(at(c.recorded), at(c.reported)); got != c.same {
			t.Errorf("%s: recorded %s, ps %s: same=%v, wanted %v",
				c.name, c.recorded, c.reported, got, c.same)
		}
	}
	day := at("12:00:00.500")
	if agent.StartMatches(day, day.Add(24*time.Hour).Truncate(time.Second)) {
		t.Error("a pid recycled a day later was taken for the step's process")
	}
}
