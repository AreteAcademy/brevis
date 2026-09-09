package components

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func day(t time.Time, total, ok, failed, open int) postgres.Day {
	return postgres.Day{Date: t, Total: total, Succeeded: ok, Failed: failed, Open: open}
}

// TestEveryDayInTheSpanGetsASquare.
//
// The grid is drawn from the DATE RANGE, not from the rows: a day with no run
// still needs a square, or the calendar closes the gap and a reader counts the
// missing week as time that did not exist.
func TestEveryDayInTheSpanGetsASquare(t *testing.T) {
	squares, _, _, _ := Calendar(nil, "w", 364)

	if len(squares) < 364 {
		t.Fatalf("%d squares for a 364-day span", len(squares))
	}
	// Every one of them is empty, and none is a link: there is nothing to open.
	for _, s := range squares {
		if !s.Empty {
			t.Fatalf("%s is not empty and no run was given", s.Date.Format("2006-01-02"))
		}
		if s.Link != "" {
			t.Fatalf("a day with no run links to %s", s.Link)
		}
	}

	// And the days are consecutive, with no repeat and no gap. An off-by-one in
	// the loop would draw a year that skips a day a week, which looks fine.
	for i := 1; i < len(squares); i++ {
		want := squares[i-1].Date.AddDate(0, 0, 1)
		if !squares[i].Date.Equal(want) {
			t.Fatalf("after %s comes %s", squares[i-1].Date.Format("2006-01-02"),
				squares[i].Date.Format("2006-01-02"))
		}
	}
}

// TestTheGridStartsOnASundayAndEndsToday.
//
// Starting exactly `span` days back would put the first column mid-week, and
// every month label one square out from the week it names.
func TestTheGridStartsOnASundayAndEndsToday(t *testing.T) {
	squares, _, _, _ := Calendar(nil, "w", 364)
	if got := squares[0].Date.Weekday(); got != time.Sunday {
		t.Errorf("the grid starts on a %s", got)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if last := squares[len(squares)-1].Date; !last.Equal(today) {
		t.Errorf("the grid ends on %s and today is %s",
			last.Format("2006-01-02"), today.Format("2006-01-02"))
	}
}

// TestTheColourIsTheWorstOutcome.
//
// The whole decision behind this chart. A day with twenty successes and one
// failure is RED: the question a calendar answers is "when did this break", and
// a colour that averaged the day away would answer "how busy was it" -- which
// the bar chart already does.
func TestTheColourIsTheWorstOutcome(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, c := range []struct {
		name string
		d    postgres.Day
		want string
	}{
		{"all succeeded", day(today, 20, 20, 0, 0), "success"},
		{"one failure among twenty", day(today, 21, 20, 1, 0), "failed"},
		{"still open", day(today, 3, 2, 0, 1), "queued"},
		{"open AND failed is failed", day(today, 5, 3, 1, 1), "failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			squares, _, _, _ := Calendar([]postgres.Day{c.d}, "w", 364)
			last := squares[len(squares)-1]
			if !strings.Contains(last.Fill, c.want) {
				t.Errorf("fill = %s, wanted the %s colour", last.Fill, c.want)
			}
			if last.Empty {
				t.Error("a day with runs was drawn as empty")
			}
		})
	}
}

// A day that ran links to that day's runs; a day that did not has nothing to
// open, and a link to an empty list is a click that teaches people not to click.
func TestOnlyADayWithRunsIsALink(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	squares, _, _, _ := Calendar([]postgres.Day{day(today, 2, 2, 0, 0)}, "daily_sales", 364)
	last := squares[len(squares)-1]

	// The list reads `from` and `to` as RFC 3339, and `to` is EXCLUSIVE. The
	// first version of this link invented `&day=`, which the list does not
	// read at all -- it would have shown every run of the workflow with the
	// date silently dropped, which is worse than no link because the reader
	// believes it.
	for _, want := range []string{
		"workflow=daily_sales",
		"from=" + today.Format(time.RFC3339),
		"to=" + today.AddDate(0, 0, 1).Format(time.RFC3339),
	} {
		if !strings.Contains(last.Link, want) {
			t.Errorf("link %q does not carry %q", last.Link, want)
		}
	}
	if strings.Contains(last.Link, "day=") {
		t.Errorf("the link uses a parameter the runs list does not read: %q", last.Link)
	}
	if squares[0].Link != "" {
		t.Errorf("an empty day links to %q", squares[0].Link)
	}
}

// The tooltip says the counts, because the colour only says the worst outcome.
// "Red" without a number is a day somebody has to go and look at.
func TestTheTooltipCarriesTheNumbers(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	squares, _, _, _ := Calendar([]postgres.Day{day(today, 21, 20, 1, 0)}, "w", 364)
	got := squares[len(squares)-1].Tooltip
	for _, want := range []string{today.Format("2006-01-02"), "21 runs", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("tooltip %q does not mention %q", got, want)
		}
	}
	// And the singular reads as a singular.
	one, _, _, _ := Calendar([]postgres.Day{day(today, 1, 1, 0, 0)}, "w", 364)
	if !strings.Contains(one[len(one)-1].Tooltip, "1 run,") {
		t.Errorf("a single run reads %q", one[len(one)-1].Tooltip)
	}
}

// The month labels sit above the week their month starts in, and there are
// roughly twelve of them over a year. A label per column would be noise; none
// at all would make the year unreadable.
func TestTheMonthsAreLabelledOnceEach(t *testing.T) {
	_, months, w, h := Calendar(nil, "w", 364)
	if len(months) < 10 || len(months) > 12 {
		t.Errorf("%d month labels over 52 weeks", len(months))
	}
	seen := map[string]bool{}
	for _, m := range months {
		if seen[m.Label] {
			t.Errorf("%s is labelled twice", m.Label)
		}
		seen[m.Label] = true
		if m.X < 0 || m.X > w {
			t.Errorf("%s is drawn at x=%d, outside a %d-wide canvas", m.Label, m.X, w)
		}
	}
	if h != calTop+7*cellStep {
		t.Errorf("height = %d, and there are seven weekdays", h)
	}
}

// Every square is inside the canvas the viewBox declares. An overflow here is
// invisible in a unit test of the numbers and shows up as a row cut in half.
func TestNothingIsDrawnOutsideTheCanvas(t *testing.T) {
	squares, _, w, h := Calendar(nil, "w", 364)
	for _, s := range squares {
		if s.X < 0 || s.X+cellSize > w || s.Y < 0 || s.Y+cellSize > h {
			t.Fatalf("%s at (%d,%d) falls outside %dx%d",
				s.Date.Format("2006-01-02"), s.X, s.Y, w, h)
		}
	}
}
