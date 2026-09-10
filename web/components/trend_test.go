package components

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func loadDay(n int, runs int, rows, bytesOut, extractMs, loadMs int64) postgres.LoadDay {
	return postgres.LoadDay{
		Date:      time.Date(2026, 3, n, 0, 0, 0, 0, time.UTC),
		Runs:      runs,
		Rows:      rows,
		BytesOut:  bytesOut,
		ExtractMs: extractMs,
		LoadMs:    loadMs,
	}
}

// A day with four runs moved four times as much, and that is a schedule getting
// denser -- not a pipeline getting bigger.
//
// This is the mistake the panel exists to avoid making: somebody switches a
// workflow from daily to every six hours, the "rows" line quadruples overnight,
// and the next person spends an afternoon looking for a source that did not
// change.
func TestVolumeIsPerRunAndNotPerDay(t *testing.T) {
	series := Trend([]postgres.LoadDay{
		loadDay(1, 1, 1000, 0, 0, 0),
		loadDay(2, 4, 4000, 0, 0, 0), // four runs, same size each
	})
	rows := series[0]
	if rows.Label != "Rows per run" {
		t.Fatalf("the first series is %q", rows.Label)
	}
	if rows.Latest != "1.0k" {
		t.Errorf("latest = %q; four runs of 1,000 is still 1,000 a run", rows.Latest)
	}
	if rows.Delta != "steady" {
		t.Errorf("delta = %q; nothing about the pipeline changed", rows.Delta)
	}
}

// The delta compares the last day against the FIRST.
//
// Day over day on a daily pipeline is one sample against one sample: a slow
// morning at the vendor reads as a 40% regression and means nothing. The
// question is about the window.
func TestTheDeltaIsAcrossTheWindowAndNotAgainstYesterday(t *testing.T) {
	// Climbs steadily, then dips on the final day. Against yesterday that dip
	// is the headline; across the window the climb is.
	series := Trend([]postgres.LoadDay{
		loadDay(1, 1, 0, 0, 0, 10000),
		loadDay(2, 1, 0, 0, 0, 14000),
		loadDay(3, 1, 0, 0, 0, 18000),
		loadDay(4, 1, 0, 0, 0, 17000),
	})
	load := series[3]
	if load.Label != "Load" {
		t.Fatalf("the fourth series is %q", load.Label)
	}
	if load.Delta != "+70%" {
		t.Errorf("delta = %q, wanted +70%% (10s to 17s across the window)", load.Delta)
	}
	// And a load taking longer is unambiguously worse, so it is painted so.
	if load.Hue != "text-state-failed" {
		t.Errorf("a load 70%% slower is drawn %q", load.Hue)
	}
}

// More rows is NOT worse, and the screen must not say it is.
//
// A pipeline whose volume doubled may be a healthy business or may be
// double-reading its source. Painting that red would be the screen guessing at
// something only the reader knows.
func TestGrowingVolumeIsNotPaintedAsAFailure(t *testing.T) {
	series := Trend([]postgres.LoadDay{
		loadDay(1, 1, 1000, 0, 0, 0),
		loadDay(2, 1, 5000, 0, 0, 0),
	})
	rows := series[0]
	if rows.Delta != "+400%" {
		t.Fatalf("delta = %q", rows.Delta)
	}
	if rows.Hue == "text-state-failed" {
		t.Error("a pipeline loading more rows was painted as a failure")
	}
}

// Bytes per row is the note's own question: a load that doubles in size while
// its row count holds flat is a schema that grew a column.
func TestBytesPerRowCatchesASchemaThatGrew(t *testing.T) {
	series := Trend([]postgres.LoadDay{
		loadDay(1, 1, 1000, 100_000, 0, 0), // 100 B a row
		loadDay(2, 1, 1000, 200_000, 0, 0), // 200 B a row, same rows
	})
	bpr := series[1]
	if bpr.Label != "Bytes per row" {
		t.Fatalf("the second series is %q", bpr.Label)
	}
	if bpr.Latest != "200 B" {
		t.Errorf("latest = %q", bpr.Latest)
	}
	if bpr.Delta != "+100%" {
		t.Errorf("delta = %q; the rows did not change and the bytes doubled", bpr.Delta)
	}
}

// One day is not a trend, and the panel says so rather than drawing a flat line
// that reads as "stable".
func TestOneDayDrawsNoLine(t *testing.T) {
	for _, s := range Trend([]postgres.LoadDay{loadDay(1, 1, 1000, 100, 5000, 5000)}) {
		if !s.Flat {
			t.Errorf("%s drew a trend from a single day", s.Label)
		}
		if s.Path != "" {
			t.Errorf("%s drew a line from a single day: %q", s.Label, s.Path)
		}
		if s.Latest == "" || s.Latest == "--" {
			t.Errorf("%s has one day of data and shows no value", s.Label)
		}
	}
}

// A series that does not move draws through the MIDDLE. Scaling a constant to
// the full height would turn rounding noise into a mountain range -- the chart
// showing a crisis in a pipeline that did the same thing every day.
func TestAConstantSeriesDrawsFlat(t *testing.T) {
	series := Trend([]postgres.LoadDay{
		loadDay(1, 1, 0, 0, 0, 12000),
		loadDay(2, 1, 0, 0, 0, 12000),
		loadDay(3, 1, 0, 0, 0, 12000),
	})
	_, h := TrendGeometry()
	load := series[3]
	ys := map[int]bool{}
	for _, p := range load.Points {
		ys[p.Y] = true
	}
	if len(ys) != 1 {
		t.Errorf("an unchanging series drew %d different heights: %v", len(ys), load.Points)
	}
	// And it draws through the MIDDLE, which is the assertion with teeth.
	//
	// "All the same height" is satisfied by a line glued to the top edge, and
	// that is exactly what dividing by a zero span produces: 0/0 is NaN, int(NaN)
	// is 0, and every point lands on the padding. Every Y agrees, the box is not
	// escaped, and the chart shows a pipeline pinned at its maximum forever.
	middle := h / 2
	for _, p := range load.Points {
		if p.Y < middle-6 || p.Y > middle+6 {
			t.Errorf("an unchanging series drew at y=%d, not near the middle (%d)", p.Y, middle)
		}
	}
	if load.Delta != "steady" {
		t.Errorf("delta = %q on a series that did not move", load.Delta)
	}
}

// Nothing to divide by. A day whose rows are zero has no bytes-per-row, and an
// infinity on a chart is worse than a gap.
func TestNothingIsDividedByZero(t *testing.T) {
	series := Trend([]postgres.LoadDay{
		loadDay(1, 0, 0, 0, 0, 0),
		loadDay(2, 0, 0, 500, 0, 0),
	})
	for _, s := range series {
		if strings.Contains(s.Latest, "Inf") || strings.Contains(s.Latest, "NaN") {
			t.Errorf("%s = %q", s.Label, s.Latest)
		}
		for _, p := range s.Points {
			if strings.Contains(p.Tooltip, "Inf") || strings.Contains(p.Tooltip, "NaN") {
				t.Errorf("%s tooltip: %q", s.Label, p.Tooltip)
			}
		}
	}
}

// No history at all: every series reports empty rather than drawing zeros. The
// page checks this before rendering the panel, and this is what makes that
// check possible.
func TestNoHistoryIsEmptyAndNotZero(t *testing.T) {
	for _, s := range Trend(nil) {
		if !s.Empty {
			t.Errorf("%s claims to have data", s.Label)
		}
		if s.Path != "" {
			t.Errorf("%s drew a line out of nothing", s.Label)
		}
	}
	if got := TrendWindow(nil); got != "" {
		t.Errorf("TrendWindow(nil) = %q", got)
	}
}

// Every point of the line stays inside the box it is drawn in. A coordinate
// outside the viewBox is a line that leaves the card.
func TestThePointsStayInsideTheViewBox(t *testing.T) {
	w, h := TrendGeometry()
	days := []postgres.LoadDay{}
	for i := 1; i <= 30; i++ {
		days = append(days, loadDay(i, 1, int64(i*i*100), int64(i*7), int64(i*300), int64(40000-i*900)))
	}
	for _, s := range Trend(days) {
		if len(s.Points) != len(days) {
			t.Errorf("%s drew %d points for %d days", s.Label, len(s.Points), len(days))
		}
		for _, p := range s.Points {
			if p.X < 0 || p.X > w || p.Y < 0 || p.Y > h {
				t.Errorf("%s: point %d,%d is outside the %dx%d box", s.Label, p.X, p.Y, w, h)
			}
		}
		// The line spans the full width: the first day at the left edge, the
		// last at the right. A chart that stops short reads as data that stops.
		if s.Points[0].X != 0 || s.Points[len(s.Points)-1].X != w {
			t.Errorf("%s spans %d..%d, not 0..%d",
				s.Label, s.Points[0].X, s.Points[len(s.Points)-1].X, w)
		}
	}
}

// The caption names the range the reader is looking at.
func TestTheWindowIsNamed(t *testing.T) {
	got := TrendWindow([]postgres.LoadDay{loadDay(1, 1, 0, 0, 0, 0), loadDay(28, 1, 0, 0, 0, 0)})
	if got != "1 Mar to 28 Mar 2026" {
		t.Errorf("TrendWindow = %q", got)
	}
	if got := TrendWindow([]postgres.LoadDay{loadDay(9, 1, 0, 0, 0, 0)}); got != "9 Mar" {
		t.Errorf("a single day reads %q", got)
	}
}
