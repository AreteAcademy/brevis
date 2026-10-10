package api_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE CONSOLE'S SHEET LOADS AFTER EVERY VENDORED ONE, OR IT DOES NOTHING.
//
// `app.css` paints the editor from this interface's own tokens:
// `.CodeMirror { background: var(--color-surface); color: var(--color-ink) }`,
// with a paragraph above it explaining that a vendored THEME would be a
// second palette to keep in step. `codemirror.css` writes `background:#fff`
// and `color:#000` under the SAME selector, and it was loading AFTER. Same
// specificity, same origin, both unlayered -- the later file wins, so every
// one of those rules was dead and the editor was drawn in the vendor's light
// palette on a console whose canvas is `#141711`.
//
// The DAG never showed this because its overrides were written as
// `.react-flow .react-flow__controls-button` -- two classes, which beats the
// vendor whatever the order. The editor's were written as one.
//
// The order is the fix rather than a specificity war: a vendored sheet is a
// DEFAULT, and this console's sheet is what overrides it. The client's own
// theme stays last of all, which is what `brand.CSS()` is for.
func TestTheConsolesSheetWinsOverTheVendoredOnes(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	sheets := regexp.MustCompile(`<link rel="stylesheet" href="(/assets/[^"]+\.css)"`).FindAllStringSubmatch(body, -1)
	ours, vendored := -1, map[string]int{}
	for i, m := range sheets {
		if m[1] == "/assets/app.css" {
			ours = i
			continue
		}
		vendored[m[1]] = i
	}
	if ours < 0 {
		t.Fatal("the screen does not load /assets/app.css at all")
	}
	if len(vendored) == 0 {
		t.Fatal("this screen loads no vendored sheet, so this test is checking nothing")
	}

	_, app := get(t, ui, "/assets/app.css")
	for href, at := range vendored {
		if at <= ours {
			continue
		}
		_, v := get(t, ui, href)
		t.Errorf("%s loads after /assets/app.css, so every rule the console writes for a selector that sheet also writes is dead.\n"+
			"Both of them write: %s", href, strings.Join(shared(app, v), ", "))
	}
}

// shared is the selectors two sheets both write, which is what turns "the
// order is wrong" into "and here is what it costs".
func shared(a, b string) []string {
	both, second := []string{}, rules(b)
	for sel := range rules(a) {
		if second[sel] {
			both = append(both, sel)
		}
	}
	sort.Strings(both)
	if len(both) > 8 {
		both = append(both[:8], "...")
	}
	if len(both) == 0 {
		return []string{"nothing today -- which is luck, not a design"}
	}
	return both
}

var selector = regexp.MustCompile(`([.#][^{}@;,]+?)\s*[{,]`)

func rules(css string) map[string]bool {
	out := map[string]bool{}
	for _, m := range selector.FindAllStringSubmatch(css, -1) {
		if sel := strings.TrimSpace(m[1]); sel != "" {
			out[sel] = true
		}
	}
	return out
}
