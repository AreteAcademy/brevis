package pages

import "testing"

// CRON IN WORDS, ONLY WHERE THE WORDS ARE CERTAIN. Each shape the translator
// claims has a row here; anything else comes back "" and the screen shows
// the expression alone. A wrong sentence under a schedule is worse than none.
func TestCronTextSaysOnlyTheShapesItIsSureOf(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{"* * * * *", "every minute"},
		{"*/1 * * * *", "every minute"},
		{"*/15 * * * *", "every 15 minutes"},
		{"5 * * * *", "hourly at :05"},
		{"0 4 * * *", "every day at 04:00"},
		{"30 18 * * *", "every day at 18:30"},
		{"0 9 * * 1-5", "weekdays at 09:00"},
		{"0 6 1 * *", "on day 1 of every month at 06:00"},
		{"  0   4 * * *  ", "every day at 04:00"},

		// Refused: shapes it does not claim, and values it cannot vouch for.
		{"", ""},
		{"0 4 * * 0", ""},
		{"0 4 * 1 *", ""},
		{"0 */2 * * *", ""},
		{"0,30 * * * *", ""},
		{"*/7 * * * *", ""}, // does not divide the hour: the gap at :56 → :00 is 4 minutes
		{"0 4 31 * *", ""},  // "every month" would be wrong in the months without a 31st
		{"60 4 * * *", ""},
		{"0 24 * * *", ""},
		{"0 4 * * MON-FRI", ""},
		{"@daily", ""},
		{"0 4 * *", ""},
	} {
		if got := CronText(c.expr); got != c.want {
			t.Errorf("CronText(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}
