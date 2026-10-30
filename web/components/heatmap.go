package components

import (
	"context"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// The calendar's geometry (screen-workflows T7): a 12px square and a 3px gap,
// 53 weeks of them. Room on the left for the weekday labels, and on top for
// the months.
const (
	calCell = 12
	calGap  = 3
	calStep = calCell + calGap
	calLeft = 30
	calTop  = 16
	// calWeeks is a year and the week it ends in.
	calWeeks = 53
)

// Square is one day of the calendar.
type Square struct {
	X, Y    int
	Date    time.Time
	Fill    string
	Tooltip string
	// Link opens the day's runs; empty for a day with none.
	Link string
	// Empty says nothing ran. It is drawn, and drawn faintly: a missing square
	// would make the grid ragged and a reader would count the gap as a week.
	Empty bool
	// Future is a day of this week still to come; Selected the day chosen.
	Future, Selected bool
}

// Month is a label above the first column of a month.
type Month struct {
	X     int
	Label string
}

// WeekdayLabel is one of the three labels down the calendar's left edge.
type WeekdayLabel struct {
	Y     int
	Label string
}

// CalendarSummary is what the calendar says before its grid: how many days
// it ran, how many failed, how many days since the last failure -- or since
// it began, if it never failed -- when that was, and since when it runs.
type CalendarSummary struct {
	RanDays, FailedDays, Streak int
	LastFailure, Since          *time.Time
}

// CalendarView is a drawn calendar: its squares and labels, its size, and
// its summary.
type CalendarView struct {
	Squares       []Square
	Months        []Month
	Weekdays      []WeekdayLabel
	Width, Height int
	Summary       CalendarSummary
}

// Calendar lays a year of days out as a grid of weeks, Monday at the top,
// ending with the week of now: 53 columns, the days still to come in this
// week drawn as such. link is where a day with runs goes; selected is the
// day chosen, marked.
//
// The colour is the day's WORST OUTCOME, not its volume, and that is the whole
// decision behind this chart. GitHub encodes how much happened, because that is
// its question. The question here is "when did this pipeline break", and a
// heatmap where a busy Tuesday and a broken Tuesday are both dark answers
// neither.
func Calendar(ctx context.Context, days []postgres.Day, now time.Time, link func(time.Time) string, selected *time.Time) CalendarView {
	byDay := make(map[string]postgres.Day, len(days))
	for _, d := range days {
		byDay[d.Date.Format("2006-01-02")] = d
	}
	today := now.UTC().Truncate(24 * time.Hour)
	// The grid's last column is this week, Monday to Sunday; its first, 52
	// weeks before.
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	start := monday.AddDate(0, 0, -7*(calWeeks-1))
	end := monday.AddDate(0, 0, 6)

	var v CalendarView
	// The month names ALREADY used: over 53 weeks a name reaches both ends of
	// the grid, and two "Oct" labels a year apart, with no year on either, is
	// a reader guessing. The second is dropped.
	used := map[string]bool{}
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		i := int(day.Sub(start).Hours() / 24)
		col, row := i/7, i%7
		if row == 0 {
			// Only when the month's first week is in view: a label on a column
			// that starts mid-month names a month mostly in the column before.
			if m := i18n.Date(ctx, day, "month"); day.Day() <= 7 && !used[m] {
				v.Months = append(v.Months, Month{X: calLeft + col*calStep, Label: m})
				used[m] = true
			}
		}
		key := day.Format("2006-01-02")
		d, ran := byDay[key]
		ran = ran && d.Total > 0
		s := Square{
			X: calLeft + col*calStep, Y: calTop + row*calStep, Date: day,
			Future:   day.After(today),
			Selected: ran && selected != nil && key == selected.Format("2006-01-02"),
		}
		when := i18n.Date(ctx, day, "day")
		switch {
		case !ran:
			s.Empty = true
			s.Fill = "var(--color-surface-2)"
			s.Tooltip = when + " · " + i18n.T(ctx, "calendar.no_run")
		case d.Failed > 0:
			s.Fill = "var(--color-state-failed)"
			s.Tooltip = when + " · " + i18n.N(ctx, "calendar.runs", d.Total) + " · " + i18n.N(ctx, "calendar.failed", d.Failed)
		case d.Open > 0:
			s.Fill = "var(--color-state-queued)"
			s.Tooltip = when + " · " + i18n.N(ctx, "calendar.runs", d.Total) + " · " + i18n.T(ctx, "calendar.open")
		default:
			s.Fill = "var(--color-state-success)"
			s.Tooltip = when + " · " + i18n.N(ctx, "calendar.runs", d.Total) + " · " + i18n.T(ctx, "calendar.ok")
		}
		if ran && link != nil {
			s.Link = link(day)
		}
		v.Squares = append(v.Squares, s)
	}
	for _, w := range []time.Weekday{time.Monday, time.Wednesday, time.Friday} {
		v.Weekdays = append(v.Weekdays, WeekdayLabel{
			Y:     calTop + ((int(w)+6)%7)*calStep + calCell - 2,
			Label: i18n.Date(ctx, monday.AddDate(0, 0, (int(w)+6)%7), "weekday"),
		})
	}
	v.Width = calLeft + calWeeks*calStep - calGap
	v.Height = calTop + 7*calStep - calGap
	v.Summary = summarise(days, today)
	return v
}

// summarise counts the days that ran and failed, and how long since the last
// failure -- or since the first run, when there was none.
func summarise(days []postgres.Day, today time.Time) CalendarSummary {
	var s CalendarSummary
	for _, d := range days {
		if d.Total == 0 {
			continue
		}
		day := d.Date.UTC().Truncate(24 * time.Hour)
		s.RanDays++
		if s.Since == nil || day.Before(*s.Since) {
			s.Since = &day
		}
		if d.Failed > 0 {
			s.FailedDays++
			if s.LastFailure == nil || day.After(*s.LastFailure) {
				s.LastFailure = &day
			}
		}
	}
	switch {
	case s.LastFailure != nil:
		s.Streak = int(today.Sub(*s.LastFailure).Hours() / 24)
	case s.Since != nil:
		s.Streak = int(today.Sub(*s.Since).Hours()/24) + 1
	}
	return s
}

// emptyOpacity keeps a day with no run visible as a grid square without letting
// it read as data, and a day to come fainter still.
func squareOpacity(s Square) string {
	switch {
	case s.Future:
		return "0.35"
	case s.Empty:
		return "0.8"
	}
	return "1"
}
