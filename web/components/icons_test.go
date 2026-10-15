package components

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func render(t *testing.T, name, class string) string {
	t.Helper()
	var b bytes.Buffer
	if err := Icon(name, class).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// EVERY GLYPH IS AN INLINE SVG IN THE TEXT'S OWN COLOUR, AND SILENT.
//
// Inline so a glyph inherits `currentColor` -- a sprite file or an <img> cannot,
// and a muted row would draw a bright icon. Hidden from a screen reader because
// an icon here always sits beside the word it illustrates, and reading both
// says the same thing twice.
func TestEveryGlyphIsAnInlineSvgInTheTextsColour(t *testing.T) {
	if len(glyphs) < 20 {
		t.Fatalf("found %d glyphs, which is not the set the screens draw", len(glyphs))
	}
	for name := range glyphs {
		got := render(t, name, "h-4 w-4")
		for _, want := range []string{`<svg`, `viewBox="0 0 24 24"`, `stroke="currentColor"`, `aria-hidden="true"`, `class="h-4 w-4 shrink-0"`} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: no %s in %s", name, want, got)
			}
		}
	}
}

// A NAME NOBODY DREW RENDERS NOTHING, rather than an empty box that holds a
// row's layout open around a gap.
func TestAnUnknownGlyphRendersNothing(t *testing.T) {
	if got := render(t, "no-such-glyph", "h-4 w-4"); got != "" {
		t.Errorf("an unknown glyph drew %q", got)
	}
}

// THE PATHS ARE DRAWING AND NOTHING ELSE. They are written into the page
// unescaped, so what they may contain is checked here once instead of trusted
// at every call.
func TestTheGlyphsAreOnlyShapes(t *testing.T) {
	tag := regexp.MustCompile(`<\s*([a-zA-Z]+)`)
	shapes := map[string]bool{"path": true, "circle": true, "rect": true, "ellipse": true, "line": true, "polyline": true, "polygon": true}
	for name, body := range glyphs {
		for _, m := range tag.FindAllStringSubmatch(body, -1) {
			if !shapes[m[1]] {
				t.Errorf("%s contains a <%s>, which is not a shape", name, m[1])
			}
		}
		if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(body) {
			t.Errorf("%s carries an event handler", name)
		}
	}
}

// EVERY GLYPH A TEMPLATE ASKS FOR EXISTS. An unknown name renders nothing on
// purpose, so a typo would ship as a quiet gap; this is what catches it.
func TestEveryGlyphATemplateAsksForExists(t *testing.T) {
	call := regexp.MustCompile(`Icon\("([a-z0-9-]+)"`)
	for _, dir := range []string{"../layouts", "../pages", "."} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.templ"))
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range call.FindAllStringSubmatch(string(src), -1) {
				if _, ok := glyphs[m[1]]; !ok {
					t.Errorf("%s asks for the glyph %q, which is not drawn", f, m[1])
				}
			}
		}
	}
}

// THE LICENCE TRAVELS WITH THE PATHS. ISC asks for the notice to be kept
// with the copies, and the copies are in this package.
func TestTheGlyphsCarryTheirLicence(t *testing.T) {
	raw, err := os.ReadFile("LICENSE-lucide")
	if err != nil {
		t.Fatalf("the Lucide licence is not beside the glyphs: %v", err)
	}
	if !strings.Contains(string(raw), "ISC License") {
		t.Error("LICENSE-lucide is not the ISC licence")
	}
}
