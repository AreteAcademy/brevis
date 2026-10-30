package components

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A PANEL IS ONE BLOCK OF A PAGE (density-base DB1): a 38px heading -- a 13px
// title and, faint beside it, a count -- over its content. One component where
// there were two (a page-local card and a Card with a caps eyebrow).
func TestAPanelHasACompactHeading(t *testing.T) {
	var b strings.Builder
	if err := Panel("Recent loads", "10").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"h-[38px]", "text-[13px]", "font-semibold", ">Recent loads<", ">10<", "text-[11.5px]"} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel lacks %s: %s", want, got)
		}
	}
}

// NO CAPS EYEBROW IS LEFT (density-base DB1): a section's title is a panel
// heading, and a label is an 11.5px muted line -- never the 0.68rem, tracked,
// uppercase accent kicker that sat on every section.
func TestNoTemplateUsesTheKicker(t *testing.T) {
	files, _ := filepath.Glob("../*/*.templ")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), `class="kicker`) || strings.Contains(string(src), `"kicker `) {
			t.Errorf("%s still uses the kicker", f)
		}
	}
}

// A DATE IN A TABLE IS SHORT, its full UTC one hover away (density-base DB2):
// `Oct 16 02:23` reads at a glance down a column; the year and the seconds
// are in the title for the row that needs them. It never wraps: on a phone
// the table scrolls, and a date broken over three lines triples the row.
func TestAShortStampKeepsTheFullDateInItsTitle(t *testing.T) {
	at := time.Date(2026, 10, 16, 2, 23, 7, 0, time.UTC)
	var b strings.Builder
	if err := ShortStamp(&at).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, ">Oct 16 02:23<") || !strings.Contains(got, `title="2026-10-16 02:23:07 UTC"`) {
		t.Errorf("short stamp = %s", got)
	}
	if !strings.Contains(got, "whitespace-nowrap") {
		t.Errorf("a short stamp must not wrap in a narrow column: %s", got)
	}
	b.Reset()
	if err := ShortStamp(nil).Render(context.Background(), &b); err != nil || !strings.Contains(b.String(), "—") {
		t.Errorf("a missing date = %q", b.String())
	}
}

// EVERY CONSOLE TABLE IS COMPACT (density-base DB2): `ct` -- 34px rows, a
// 30px sentence-case header -- on each table but the SQL result grid, which
// has its own density; and no header in tracked capitals.
func TestEveryConsoleTableIsCompact(t *testing.T) {
	files, _ := filepath.Glob("../pages/*.templ")
	for _, f := range files {
		if strings.HasSuffix(f, "sql.templ") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "<table") && !strings.Contains(line, `"ct `) && !strings.Contains(line, ` ct"`) && !strings.Contains(line, ` ct `) {
				t.Errorf("%s: a table is not ct: %s", f, strings.TrimSpace(line))
			}
		}
		if strings.Contains(string(src), "uppercase tracking-[0.12em]") {
			t.Errorf("%s: a header is still in tracked capitals", f)
		}
	}
}

// A STATE IS A MARK AND A WORD (density-base DB3), in the state's colour --
// not a tinted badge in every row. The mark is the state's shape, drawn by
// the stylesheet in the word's colour (see status.go). A retry is `↻N`, its meaning in the
// tooltip, and nothing on a first try (attempt counts from 0).
func TestAStateIsADotAndAWordAndARetryIsMarked(t *testing.T) {
	var b strings.Builder
	if err := State("failed").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Contains(got, "bg-state-failed/13") || strings.Contains(got, "rounded-card") {
		t.Errorf("the state is still a badge: %s", got)
	}
	for _, want := range []string{"st-diamond", "text-state-failed", "failed</span>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the state lacks %s: %s", want, got)
		}
	}
	b.Reset()
	if err := Retry(0).Render(context.Background(), &b); err != nil || b.String() != "" {
		t.Errorf("a first try is marked: %q", b.String())
	}
	b.Reset()
	if err := Retry(1).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "↻2") || !strings.Contains(b.String(), `title="attempt 2 of this run"`) {
		t.Errorf("a retry = %s", b.String())
	}
}

// THE KPI STRIP AT v2 MEASURES (density-base DB4): an 11.5px label (with the
// state's dot when it has one), an 18px value with its note inline and faint,
// and an optional thin bar of segments.
func TestTheKPIStripIsCompact(t *testing.T) {
	var b strings.Builder
	err := Stats([]Stat{{Label: "Success rate", Value: "100.0%", Note: "11 of 11 finished", Dot: "bg-state-success",
		Bar: []BarSegment{{Share: 0.9, Class: "bg-state-success"}, {Share: 0.1, Class: "bg-state-failed"}}}}).Render(context.Background(), &b)
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"text-[18px]", "text-[11.5px]", "bg-state-success", "100.0%", "11 of 11 finished",
		`style="width:90%;"`, `style="width:10%;"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the strip lacks %s: %s", want, got)
		}
	}
	value := got[strings.Index(got, "<dd"):]
	value = value[:strings.Index(value, "</dd>")]
	if !strings.Contains(value, "11 of 11 finished") {
		t.Error("the note is not inline with the value")
	}
}

// THE SMALL CHARTS ARE SMALL (density-base DB5): rows per load is 44px tall
// with bars of about 22px, not stretched across the panel; the year of runs is
// 12px squares at their own size (screen-workflows T7), 53 weeks in about
// 820px.
func TestTheSmallChartsAreSmall(t *testing.T) {
	four := int64(4)
	var b strings.Builder
	if err := LoadBars([]LoadBar{{Href: "/runs/a", Rows: &four}, {Href: "/runs/b", Rows: &four}, {Href: "/runs/c", Rows: &four}}, 0).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `class="h-11"`) || !strings.Contains(b.String(), "width:min(100%,") {
		t.Errorf("the bars are not 44px at their own width: %s", b.String()[:160])
	}

	b.Reset()
	if err := Heatmap(Calendar(context.Background(), nil, time.Now(), nil, nil)).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, `width="12"`) || strings.Contains(got, "h-[132px]") {
		t.Errorf("the heatmap is not 12px squares at its own size")
	}
	w := got[strings.Index(got, `<svg width="`)+len(`<svg width="`):]
	w = w[:strings.Index(w, `"`)]
	if n, _ := strconv.Atoi(w); n < 780 || n > 860 {
		t.Errorf("the heatmap is %spx wide, want about 820", w)
	}
}
