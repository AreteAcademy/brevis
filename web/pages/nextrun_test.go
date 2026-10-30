package pages

import (
	"context"
	"testing"
	"time"
)

// WHEN IT RUNS NEXT, by a fixed clock: "in 12m" with the UTC time when the
// schedule runs -- minutes under an hour, hours and minutes under two days,
// days after that. Computed on the server; nothing ticks.
func TestTheNextRunIsSaidByTheClock(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 48, 0, 0, time.UTC)
	for _, c := range []struct {
		in         time.Duration
		value, sub string
	}{
		{12 * time.Minute, "in 12m", "Oct 17 04:00 UTC"},
		{3*time.Hour + 20*time.Minute, "in 3h 20m", "Oct 17 07:08 UTC"},
		{50 * time.Hour, "in 2d", "Oct 19 05:48 UTC"},
		{20 * time.Second, "in <1m", "Oct 17 03:48 UTC"},
	} {
		value, sub := nextRunText(context.Background(), now.Add(c.in), now)
		if value != c.value || sub != c.sub {
			t.Errorf("%s away: got %q %q, want %q %q", c.in, value, sub, c.value, c.sub)
		}
	}
}
