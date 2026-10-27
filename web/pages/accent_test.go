package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE ACCENT IS SPENT IN FEW PLACES, OR IT STOPS POINTING AT ANYTHING.
//
// The lime says "this is the thing to do here", and it says it only if it is
// rare: the one primary action, the open section, the brand's mark and the
// focus ring. A link, a chip, a tab, a bar or a chart line in the same colour
// competes with the button, and a screen where everything is lime has no
// primary action at all. Links are ink, underlined; bars and charts are
// neutral; a selected tab is underlined in ink.
//
// Every utility that paints with the accent (`accent`, `gold`, either
// `-strong` or `-wash`) in the templates and in the Go that writes classes or
// CSS (flow.go, bars.go, the tab CSS in ingest.go) is checked against this
// list of the places that may.
func TestTheAccentIsSpentOnlyWhereItPointsAtSomething(t *testing.T) {
	allowed := map[string]map[string]bool{
		// The brand's mark beside its name, here and on the sign-in page.
		"text-accent": {"layouts/base.templ": true, "pages/login.templ": true},
		// The one primary action -- the Button's, and the sign-in page's own.
		"bg-accent-strong":          {"components/controls.go": true, "pages/login.templ": true},
		"hover:bg-accent-strong/90": {"components/controls.go": true, "pages/login.templ": true},
		// The open section's bar.
		"shadow-[inset_2px_0_0_var(--color-accent-strong)]": {"layouts/base.templ": true},
	}
	paint := regexp.MustCompile(`(?:[a-z-]+:)*(?:text|bg|border|fill|stroke|accent|decoration|ring|shadow|outline)-(?:\[[^\]\s"]*(?:accent|gold)[^\]\s"]*\]|(?:accent|gold)(?:-strong|-wash)?(?:/\d+)?\b)|var\(--color-(?:accent|gold)[a-z-]*\)`)

	templates, _ := filepath.Glob("../*/*.templ")
	code, _ := filepath.Glob("../*/*.go")
	files := templates
	for _, f := range code {
		if !strings.HasSuffix(f, "_templ.go") && !strings.HasSuffix(f, "_test.go") {
			files = append(files, f)
		}
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rel := strings.TrimPrefix(f, "../")
		for n, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, use := range paint.FindAllString(line, -1) {
				// The focus ring is the accent's third job, everywhere.
				if strings.HasPrefix(use, "focus:") || strings.HasPrefix(use, "focus-visible:") {
					continue
				}
				if allowed[use][rel] {
					continue
				}
				t.Errorf("%s:%d paints with the accent: %s", rel, n+1, use)
			}
		}
	}
}
