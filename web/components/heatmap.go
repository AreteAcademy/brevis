package components

import (
	"fmt"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// The calendar's geometry. A square and its gap, in the SVG's own units, so the
// whole drawing scales with one viewBox and needs no media query.
const (
	cellSize = 11
	cellGap  = 3
	cellStep = cellSize + cellGap
	// Room on the left for the weekday labels, and on top for the months.
	calLeft = 26
	calTop  = 14
)

// Square is one day of the calendar.
type Square struct {
	X, Y    int
	Date    time.Time
	Fill    string
	Tooltip string
	Link    string
	// Empty says nothing ran. It is drawn, and drawn faintly: a missing square
	// would make the grid ragged and a reader would count the gap as a week.
	Empty bool
}

// Month is a label above the first column of a month.
type Month struct {
	X     int
	Label string
}

// Calendar lays a year of days out as a GitHub-style grid: one column per week,
// Sunday at the top.
//
// The colour is the day's WORST OUTCOME, not its volume, and that is the whole
// decision behind this chart. GitHub encodes how much happened, because that is
// its question. The question here is "when did this pipeline break", and a
// heatmap where a busy Tuesday and a broken Tuesday are both dark answers
// neither. Volume is what the bar chart above already draws.
func Calendar(days []postgres.Day, workflow string, span int) ([]Square, []Month, int, int) {
	if span <= 0 {
		span = 364 // 52 weeks, which is the canonical "a year" grid
	}
	byDay := make(map[string]postgres.Day, len(days))
	for _, d := range days {
		byDay[d.Date.Format("2006-01-02")] = d
	}

	// The grid ends TODAY and starts on the Sunday that keeps the columns
	// whole. Starting exactly `span` days back would put the first column
	// mid-week and every month label one square out.
	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -(span - 1))
	start = start.AddDate(0, 0, -int(start.Weekday()))

	var squares []Square
	var months []Month
	// The month names ALREADY used, and not just the previous one.
	//
	// Over 52 weeks the same name reaches both ends of the grid -- a span that
	// starts in September ends in September -- and two "Sep" labels a year
	// apart, with no year on either, is a reader guessing. The second is
	// dropped: the last column is today, and nobody needs to be told what month
	// today is.
	used := map[string]bool{}
	col := 0

	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Sunday {
			col = int(day.Sub(start).Hours()/24) / 7
			// Only when the month's first week is actually in view: a label
			// on a column that starts mid-month would name a month whose
			// squares are mostly in the column before it.
			if m := day.Format("Jan"); day.Day() <= 7 && !used[m] {
				months = append(months, Month{X: calLeft + col*cellStep, Label: m})
				used[m] = true
			}
		}
		key := day.Format("2006-01-02")
		d, ran := byDay[key]

		s := Square{
			X:    calLeft + col*cellStep,
			Y:    calTop + int(day.Weekday())*cellStep,
			Date: day,
		}
		switch {
		case !ran || d.Total == 0:
			s.Empty = true
			s.Fill = "var(--color-line)"
			s.Tooltip = day.Format("2006-01-02") + ": no run"
		case d.Failed > 0:
			s.Fill = "var(--color-state-failed)"
			s.Tooltip = fmt.Sprintf("%s: %s, %d failed",
				key, plural(d.Total, "run", "runs"), d.Failed)
		case d.Open > 0:
			s.Fill = "var(--color-state-queued)"
			s.Tooltip = fmt.Sprintf("%s: %s, %d still open",
				key, plural(d.Total, "run", "runs"), d.Open)
		default:
			s.Fill = "var(--color-state-success)"
			s.Tooltip = fmt.Sprintf("%s: %s, all succeeded", key, plural(d.Total, "run", "runs"))
		}
		if ran && d.Total > 0 && workflow != "" {
			// The runs list takes `from` and `to` as RFC 3339, and `to` is
			// EXCLUSIVE -- `criado_em < $ate`. So a day is [midnight, next
			// midnight), which is why the second bound is tomorrow.
			//
			// The first version of this link invented `&day=`, which the list
			// does not read: it would have shown every run of the workflow with
			// the date silently dropped. A link that goes to the wrong place is
			// worse than a square that is not clickable, because the reader
			// believes it.
			s.Link = fmt.Sprintf("/runs?workflow=%s&from=%s&to=%s",
				workflow,
				day.Format(time.RFC3339),
				day.AddDate(0, 0, 1).Format(time.RFC3339))
		}
		squares = append(squares, s)
	}

	width := calLeft + (col+1)*cellStep
	height := calTop + 7*cellStep
	return squares, months, width, height
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// WeekdayLabel is one of the three labels down the calendar's left edge.
type WeekdayLabel struct {
	Y     int
	Label string
}

// weekdayLabels returns Mon, Wed and Fri. Seven labels beside eleven-pixel
// squares is more text than grid.
func weekdayLabels() []WeekdayLabel {
	return []WeekdayLabel{
		{Y: calTop + int(time.Monday)*cellStep + 9, Label: "Mon"},
		{Y: calTop + int(time.Wednesday)*cellStep + 9, Label: "Wed"},
		{Y: calTop + int(time.Friday)*cellStep + 9, Label: "Fri"},
	}
}

// emptyOpacity keeps a day with no run visible as a grid square without letting
// it read as data. Fully opaque, the line colour is close enough to a pale
// success to be misread at a glance.
func emptyOpacity(empty bool) string {
	if empty {
		return "0.55"
	}
	return "1"
}
