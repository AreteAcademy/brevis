package components

import (
	"context"
	"os"
	"path/filepath"
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
// are in the title for the row that needs them.
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
