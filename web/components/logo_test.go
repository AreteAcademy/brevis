package components

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// EVERY MARK IN THE REGISTRY IS DRAWN, NAMED, AND FROM THE BINARY. The
// console runs where there is no internet, so a logo is an SVG embedded at
// build time and written inline -- never a URL -- and it says whose it is to
// whoever cannot see it.
func TestEveryRegisteredLogoIsDrawnInlineAndNamed(t *testing.T) {
	if n := len(logos) - countMonograms(); n < 28 {
		t.Fatalf("%d marks registered; the handoff's 27 and brevis-sql's are 28", n)
	}
	for key, l := range logos {
		if l.monogram != "" {
			continue
		}
		html := draw(t, Logo(key, 18))
		if !strings.Contains(html, "<svg") {
			t.Errorf("%s draws no SVG: %s", key, html)
		}
		if !strings.Contains(html, `role="img"`) || !strings.Contains(html, `aria-label="`+l.title+`"`) {
			t.Errorf("%s is not named %q: %.200s", key, l.title, html)
		}
		if strings.Contains(html, "<img") {
			t.Errorf("%s is drawn as an <img>, which would fetch it", key)
		}
	}
}

// THE CLASSES SAY HOW THE MARK IS PAINTED: in its own colours (multi), or in
// one, darkened when the brand is too light for a light ground and drawn in
// ink when it is too dark for a dark one.
func TestALogoCarriesItsSizeAndHowItIsPainted(t *testing.T) {
	for size, class := range map[int]string{18: "cg-18", 28: "cg-28", 40: "cg-40"} {
		if html := draw(t, Logo("bigquery", size)); !strings.Contains(html, class) {
			t.Errorf("size %d lacks %s: %.200s", size, class, html)
		}
	}
	for key, class := range map[string]string{"python": "cg-multi", "kafka": "cg-dark", "duckdb": "cg-light", "pandas": "cg-on-white"} {
		if html := draw(t, Logo(key, 18)); !strings.Contains(html, class) {
			t.Errorf("%s lacks %s: %.200s", key, class, html)
		}
	}
}

// A KEY WITH NO MARK IS A MONOGRAM, never a broken image and never somebody
// else's logo: the handoff's own (http as {}, the gateway as an arrow) and,
// for anything unknown, its first letters.
func TestAKeyWithNoMarkIsAMonogram(t *testing.T) {
	for key, text := range map[string]string{"http": "{}", "gateway": "→", "sftp": "SF", "acme_crm": "AC"} {
		html := draw(t, Logo(key, 18))
		if strings.Contains(html, "<svg") || !strings.Contains(html, "cg-mono") || !strings.Contains(html, ">"+text+"<") {
			t.Errorf("%s is not the monogram %q: %s", key, text, html)
		}
	}
}

// THE FILES ARE ONLY DRAWINGS. They are written into the page unescaped, so
// nothing in them may run, fetch, or point outside itself; and a gradient id
// repeated across two marks on one page would paint one with the other's
// colours, so every id is unique.
func TestTheLogoFilesAreOnlyDrawingsWithUniqueIDs(t *testing.T) {
	external := regexp.MustCompile(`https?://`)
	namespace := regexp.MustCompile(`xmlns(:xlink)?="http://www\.w3\.org/[0-9]+/(svg|xlink)"`)
	active := regexp.MustCompile(`(?i)<script|<foreignObject|\son[a-z]+=|href="[^#]`)
	ids := map[string]string{}
	files, err := fs.Glob(logoFiles, "logos/*.svg")
	if err != nil || len(files) == 0 {
		t.Fatalf("no embedded logo files (%v)", err)
	}
	if len(files) != len(logos)-countMonograms() {
		t.Errorf("%d files for %d drawn logos", len(files), len(logos)-countMonograms())
	}
	for _, f := range files {
		raw, _ := fs.ReadFile(logoFiles, f)
		svg := namespace.ReplaceAllString(string(raw), "")
		if external.MatchString(svg) {
			t.Errorf("%s points outside itself", f)
		}
		if active.MatchString(svg) {
			t.Errorf("%s carries something that runs or links", f)
		}
		for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllStringSubmatch(svg, -1) {
			if other, dup := ids[m[1]]; dup {
				t.Errorf("id %q is in %s and %s", m[1], other, f)
			}
			ids[m[1]] = f
		}
	}
}

// THE MARKS ARE OTHER PEOPLE'S WORK, and their licences travel with them.
func TestTheLogosLicencesAreBesideThem(t *testing.T) {
	for _, f := range []string{"LICENSE-devicon", "LICENSE-simple-icons"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s is missing: %v", f, err)
			continue
		}
		if !strings.Contains(string(raw), "trademark") {
			t.Errorf("%s does not say the marks belong to their owners", f)
		}
	}
}
