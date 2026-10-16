package components

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
