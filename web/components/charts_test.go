package components

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func bucketsWith(totais ...int) []postgres.Bucket {
	base := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	out := make([]postgres.Bucket, len(totais))
	for i, n := range totais {
		out[i] = postgres.Bucket{Start: base.Add(time.Duration(i) * time.Hour), Succeeded: n}
	}
	return out
}

// The axis has to land on round numbers AND ones divisible by four. The previous
// version divided the raw maximum and produced labels 0/6/12/18/25 -- every
// interval different from the one before it.
func TestTheScalesCeilingIsRoundAndDivisibleByFour(t *testing.T) {
	casos := []struct{ max, expected int }{
		{0, 4}, {1, 4}, {3, 4}, {4, 4}, {5, 8}, {27, 40}, {40, 40}, {41, 100}, {600, 1000},
	}
	for _, c := range casos {
		got := ceiling(bucketsWith(c.max))
		if got != c.expected {
			t.Errorf("teto(%d) = %d, want %d", c.max, got, c.expected)
		}
		if got%4 != 0 {
			t.Errorf("ceiling(%d) = %d is not divisible by 4: the labels would come out broken", c.max, got)
		}
		if got < c.max {
			t.Errorf("ceiling(%d) = %d cuts the tallest column off", c.max, got)
		}
	}
}

func TestTheGridLinesAreEquidistant(t *testing.T) {
	rows := gridLines(bucketsWith(27))
	if len(rows) != 5 {
		t.Fatalf("obtive %d linhas, want 5", len(rows))
	}
	// A 1px tolerance: the division is integer, so a 214px area in four
	// faixas alterna 53 e 54. Exigir igualdade exata testaria o arredondamento,
	// not the grid.
	step := rows[0].Y - rows[1].Y
	for i := 1; i < len(rows)-1; i++ {
		if d := rows[i].Y - rows[i+1].Y; d < step-1 || d > step+1 {
			t.Fatalf("grade irregular: %v", rows)
		}
	}
	if rows[0].Label != "0" || rows[4].Label != "40" {
		t.Errorf("rotulos = %s .. %s, want 0 .. 40", rows[0].Label, rows[4].Label)
	}
}

// A single failure among hundreds of successes still has to be seen -- it is the
// case in which the chart matters most.
func TestTheSmallestBarSurvives(t *testing.T) {
	buckets := []postgres.Bucket{{Succeeded: 400, Failed: 1}}
	b := bars(buckets)[0]
	if b.HFailed < 2 {
		t.Errorf("altura da falha = %d, sumiria da tela", b.HFailed)
	}
	if b.YFailed+b.HFailed != b.YSucceeded {
		t.Errorf("pilha desalinhada: falha termina em %d e sucesso comeca em %d",
			b.YFailed+b.HFailed, b.YSucceeded)
	}
}

// The duration curve must not join two peaks across an empty hour: that would
// invent duration where there was no run at all.
func TestTheDurationLineBreaksOnAGap(t *testing.T) {
	base := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	buckets := []postgres.Bucket{
		{Start: base, Succeeded: 1, MeanDuration: time.Second},
		{Start: base.Add(time.Hour)},
		{Start: base.Add(2 * time.Hour), Succeeded: 1, MeanDuration: 2 * time.Second},
	}
	d := durationLine(buckets)
	if countM(d) != 2 {
		t.Errorf("path %q should have two starts (M), one per segment", d)
	}

	if durationLine(bucketsWith(3)) != "" {
		t.Error("with no measured duration there should be no curve")
	}
}

func countM(s string) int {
	n := 0
	for _, r := range s {
		if r == 'M' {
			n++
		}
	}
	return n
}

func TestTheDurationPicksItsUnit(t *testing.T) {
	casos := []struct {
		d        time.Duration
		expected string
	}{
		{400 * time.Millisecond, "400ms"},
		{2500 * time.Millisecond, "2.5s"},
		{90 * time.Second, "1m 30s"},
		{3*time.Hour + 4*time.Minute, "3h 04m"},
	}
	for _, c := range casos {
		if got := Duration(&c.d); got != c.expected {
			t.Errorf("Duration(%s) = %s, want %s", c.d, got, c.expected)
		}
	}
	if Duration(nil) != "—" {
		t.Error("a missing duration should become a dash, not a zero")
	}
}

// A DAY'S BAR READS AS A DAY (screen-overview-v2 OV3): the chart takes its
// grain from its buckets -- a day apart is a day -- and labels, tooltips and
// links follow: "Oct 17", the runs of that whole day.
func TestADayBucketReadsAsADay(t *testing.T) {
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	var buckets []postgres.Bucket
	for i := 0; i < 7; i++ {
		buckets = append(buckets, postgres.Bucket{Start: day.Add(time.Duration(i) * 24 * time.Hour), Succeeded: i})
	}
	var b strings.Builder
	if err := RunsChart(buckets).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, ">Oct 17<") || strings.Contains(got, "00h<") {
		t.Errorf("a day bar is not labelled by its day")
	}
	if !strings.Contains(got, `aria-label="Runs per day"`) {
		t.Error("the chart does not say it is per day")
	}
	want := "/runs?de=" + url.QueryEscape("2026-10-17T00:00:00Z") + "&amp;ate=" + url.QueryEscape("2026-10-18T00:00:00Z")
	if !strings.Contains(got, want) {
		t.Errorf("the last bar does not open its whole day (%s)", want)
	}
}
