package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

const (
	typePeriod = "period"
	startDate  = "start_date"
	endDate    = "end_date"
)

// periods are what a `period` parameter accepts, in the order a refusal
// lists them.
//
// `last_7_days` and `last_30_days` are COMPLETE days, today excluded. The
// data behind an endpoint arrives in batches, so today is always partial,
// and a "last seven days" holding a third of today draws a drop on the last
// bar of every chart that reads it. `today` and `month_to_date` say they
// include today in their names. Weeks are ISO: Monday first.
var periods = []string{"today", "yesterday", "last_7_days", "last_30_days", "last_week", "last_month", "month_to_date"}

// resolvePeriod is the first and last day a period names, as of `now` in
// `loc`.
//
// CALENDAR ARITHMETIC AND NEVER DURATIONS. Today is read off the clock in the
// file's timezone, and from there every step is AddDate on a date at UTC
// midnight -- so a night that is 23 or 25 hours long, which is what a
// timezone with daylight saving has twice a year, moves nothing.
func resolvePeriod(period string, now time.Time, loc *time.Location) (dialect.Date, dialect.Date, error) {
	y, m, d := now.In(loc).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	var from, to time.Time
	switch period {
	case "today":
		from, to = today, today
	case "yesterday":
		from = today.AddDate(0, 0, -1)
		to = from
	case "last_7_days":
		from, to = today.AddDate(0, 0, -7), today.AddDate(0, 0, -1)
	case "last_30_days":
		from, to = today.AddDate(0, 0, -30), today.AddDate(0, 0, -1)
	case "last_week":
		// Monday of this week, then the seven days before it. Go's Sunday
		// is 0; ISO's is 7.
		sinceMonday := (int(today.Weekday()) + 6) % 7
		monday := today.AddDate(0, 0, -sinceMonday)
		from, to = monday.AddDate(0, 0, -7), monday.AddDate(0, 0, -1)
	case "last_month":
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		from, to = first.AddDate(0, -1, 0), first.AddDate(0, 0, -1)
	case "month_to_date":
		from, to = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC), today
	default:
		return dialect.Date{}, dialect.Date{}, fmt.Errorf("one of: %s", strings.Join(periods, ", "))
	}
	return civil(from), civil(to), nil
}

func civil(t time.Time) dialect.Date {
	return dialect.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// checkPeriod refuses a period an endpoint could not honour: it sets
// start_date and end_date, so both have to be there, as dates, and one
// period is the most an endpoint can have -- two would be two answers to
// "which days".
func (e *Endpoint) checkPeriod() error {
	var found []string
	for _, name := range sortedNames(e.Params) {
		if e.Params[name].Type == typePeriod {
			found = append(found, name)
		}
	}
	switch {
	case len(found) == 0:
		return nil
	case len(found) > 1:
		return fmt.Errorf("endpoint %s declares %s: an endpoint takes one period", e.Name, strings.Join(found, " and "))
	}
	for _, name := range []string{startDate, endDate} {
		if p, there := e.Params[name]; !there || p.Type != typeDate {
			return fmt.Errorf("endpoint %s: the period %s sets start_date and end_date, and this "+
				"endpoint declares no %s of type date", e.Name, found[0], name)
		}
	}
	e.period = found[0]
	return nil
}
