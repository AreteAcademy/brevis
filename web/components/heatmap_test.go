package components

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

func day(t time.Time, total, ok, failed, open int) postgres.Day {
	return postgres.Day{Date: t, Total: total, Succeeded: ok, Failed: failed, Open: open}
}

// thursday is a fixed today, 2026-10-29, a Thursday.
var thursday = time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC)

func dayLink(d time.Time) string { return "/w?day=" + d.Format("2006-01-02") }

func calendar(days []postgres.Day) CalendarView {
	return Calendar(context.Background(), days, thursday, dayLink, nil)
}

// A YEAR IS 53 WEEKS, MONDAY TO SUNDAY, ending with this week: every day gets
// a square, in order, with no gap -- a missing day would close the grid and a
// reader would count the week as time that did not exist -- and the days of
// this week still to come are drawn, as days to come.
func TestTheCalendarIsFiftyThreeWeeksMondayFirst(t *testing.T) {
	v := calendar(nil)
	if len(v.Squares) != 53*7 {
		t.Fatalf("%d squares, want 53 weeks of 7", len(v.Squares))
	}
	first, last := v.Squares[0].Date, v.Squares[len(v.Squares)-1].Date
	if first.Weekday() != time.Monday || last.Weekday() != time.Sunday {
		t.Errorf("the grid runs %s to %s, want Monday to Sunday", first.Weekday(), last.Weekday())
	}
	if !last.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the grid ends %s, want this week's Sunday", last)
	}
	for i := 1; i < len(v.Squares); i++ {
		if !v.Squares[i].Date.Equal(v.Squares[i-1].Date.AddDate(0, 0, 1)) {
			t.Fatalf("a gap after %s", v.Squares[i-1].Date)
		}
	}
	for _, s := range v.Squares {
		if s.Date.Weekday() == time.Monday && s.Y != calTop {
			t.Fatalf("a Monday at y=%d, want the top row", s.Y)
		}
		if future := s.Date.After(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)); s.Future != future {
			t.Fatalf("%s: future = %v", s.Date.Format("2006-01-02"), s.Future)
		}
	}
}

// THE COLOUR IS THE DAY'S WORST OUTCOME, not its volume: the question is when
// it broke. A failure outranks a run still open, which outranks success.
func TestTheColourIsTheWorstOutcome(t *testing.T) {
	d := func(n int) time.Time { return time.Date(2026, 10, n, 0, 0, 0, 0, time.UTC) }
	v := calendar([]postgres.Day{
		day(d(20), 5, 5, 0, 0), day(d(21), 5, 3, 1, 1), day(d(22), 5, 4, 0, 1),
	})
	fill := map[string]string{}
	for _, s := range v.Squares {
		fill[s.Date.Format("2006-01-02")] = s.Fill
	}
	for date, want := range map[string]string{
		"2026-10-20": "var(--color-state-success)",
		"2026-10-21": "var(--color-state-failed)",
		"2026-10-22": "var(--color-state-queued)",
		"2026-10-23": "var(--color-surface-2)",
	} {
		if fill[date] != want {
			t.Errorf("%s is %s, want %s", date, fill[date], want)
		}
	}
}

// ONLY A DAY WITH RUNS OPENS THEM, where its caller says; the day chosen is
// marked.
func TestOnlyADayWithRunsIsALink(t *testing.T) {
	ran := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	v := Calendar(context.Background(), []postgres.Day{day(ran, 1, 1, 0, 0)}, thursday, dayLink, &ran)
	for _, s := range v.Squares {
		switch {
		case s.Date.Equal(ran):
			if s.Link != "/w?day=2026-10-20" || !s.Selected {
				t.Errorf("the day that ran: link %q, selected %v", s.Link, s.Selected)
			}
		case s.Link != "" || s.Selected:
			t.Fatalf("%s: link %q, selected %v", s.Date.Format("2006-01-02"), s.Link, s.Selected)
		}
	}
}

// THE TOOLTIP SAYS THE DAY, HOW MANY RAN AND HOW IT WENT, in the reader's
// language.
func TestTheTooltipSaysTheDayInTheReadersLanguage(t *testing.T) {
	days := []postgres.Day{day(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), 1, 1, 0, 0)}
	for lang, want := range map[string]string{
		"en":    "Thu, Oct 29, 2026 · 1 run · all succeeded",
		"pt-BR": "qui, 29 out 2026 · 1 execução · tudo certo",
	} {
		v := Calendar(i18n.WithLang(context.Background(), lang), days, thursday, dayLink, nil)
		var got string
		for _, s := range v.Squares {
			if s.Date.Day() == 29 && s.Date.Month() == time.October && s.Date.Year() == 2026 {
				got = s.Tooltip
			}
		}
		if got != want {
			t.Errorf("%s: %q, want %q", lang, got, want)
		}
	}
}

// THE SUMMARY ANSWERS THE QUESTIONS BEFORE THE GRID: how many days it ran,
// how many failed, how long since the last failure, when that was, and since
// when it runs.
func TestTheCalendarSummarisesTheYear(t *testing.T) {
	d := func(m time.Month, n int) time.Time { return time.Date(2026, m, n, 0, 0, 0, 0, time.UTC) }
	v := calendar([]postgres.Day{
		day(d(9, 1), 2, 2, 0, 0), day(d(10, 9), 2, 1, 1, 0), day(d(10, 28), 1, 1, 0, 0),
	})
	s := v.Summary
	if s.RanDays != 3 || s.FailedDays != 1 || s.Streak != 20 {
		t.Errorf("summary = %+v, want 3 days, 1 failed, 20 in a row", s)
	}
	if s.LastFailure == nil || !s.LastFailure.Equal(d(10, 9)) || s.Since == nil || !s.Since.Equal(d(9, 1)) {
		t.Errorf("last failure %v, since %v", s.LastFailure, s.Since)
	}

	clean := calendar([]postgres.Day{day(d(10, 20), 1, 1, 0, 0)})
	if clean.Summary.LastFailure != nil || clean.Summary.Streak != 10 {
		t.Errorf("a workflow that never failed: %+v, want 10 days since it began", clean.Summary)
	}
}

// THE MONTHS ARE LABELLED ONCE EACH, in the reader's language, inside the
// canvas, and nothing is drawn outside it.
func TestTheMonthsAreLabelledOnceEach(t *testing.T) {
	v := Calendar(i18n.WithLang(context.Background(), "pt-BR"), nil, thursday, dayLink, nil)
	if len(v.Months) < 11 || len(v.Months) > 13 {
		t.Errorf("%d month labels over 53 weeks", len(v.Months))
	}
	seen := map[string]bool{}
	for _, m := range v.Months {
		if seen[m.Label] {
			t.Errorf("%s is labelled twice", m.Label)
		}
		seen[m.Label] = true
		if m.X < 0 || m.X > v.Width {
			t.Errorf("%s at x=%d, outside %d", m.Label, m.X, v.Width)
		}
	}
	if !seen["out"] && !seen["set"] {
		t.Errorf("the months are not Portuguese: %v", v.Months)
	}
	labels := []string{}
	for _, w := range v.Weekdays {
		labels = append(labels, w.Label)
	}
	if strings.Join(labels, " ") != "seg qua sex" {
		t.Errorf("the weekdays are %v", labels)
	}
	for _, s := range v.Squares {
		if s.X < 0 || s.X+calCell > v.Width || s.Y < 0 || s.Y+calCell > v.Height {
			t.Fatalf("%s at (%d,%d) falls outside %dx%d", s.Date.Format("2006-01-02"), s.X, s.Y, v.Width, v.Height)
		}
	}
}
