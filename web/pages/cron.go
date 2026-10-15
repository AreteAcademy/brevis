package pages

import (
	"fmt"
	"strconv"
	"strings"
)

// CronText says a schedule in words -- "every day at 04:00" -- for the few
// shapes that cover most schedules, and "" for anything else, which the
// screen then shows as the bare expression. It never guesses: a sentence
// that is almost right under a schedule is how somebody misses the run that
// mattered.
//
// The shapes: every N minutes (N dividing the hour), hourly at :MM, every
// day, weekdays (1-5) and one day of the month (up to the 28th, which every
// month has) at HH:MM. The hour is the schedule's own timezone's.
func CronText(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return ""
	}
	min, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	if month != "*" {
		return ""
	}

	if hour == "*" && dom == "*" && dow == "*" {
		if min == "*" {
			return "every minute"
		}
		if step, ok := strings.CutPrefix(min, "*/"); ok {
			n, ok := number(step, 1, 30)
			if !ok || 60%n != 0 {
				return ""
			}
			if n == 1 {
				return "every minute"
			}
			return fmt.Sprintf("every %d minutes", n)
		}
		if m, ok := number(min, 0, 59); ok {
			return fmt.Sprintf("hourly at :%02d", m)
		}
		return ""
	}

	m, okM := number(min, 0, 59)
	h, okH := number(hour, 0, 23)
	if !okM || !okH {
		return ""
	}
	at := fmt.Sprintf("%02d:%02d", h, m)
	switch {
	case dom == "*" && dow == "*":
		return "every day at " + at
	case dom == "*" && dow == "1-5":
		return "weekdays at " + at
	case dow == "*":
		if d, ok := number(dom, 1, 28); ok {
			return fmt.Sprintf("on day %d of every month at %s", d, at)
		}
	}
	return ""
}

// number reads a plain cron number in [lo, hi]: digits only, no list, range
// or step.
func number(s string, lo, hi int) (int, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}
