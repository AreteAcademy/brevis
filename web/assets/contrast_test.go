package assets

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// CONTRAST IS MEASURED, NOT LOOKED AT.
//
// A palette is where text quietly becomes unreadable: nothing errors, nothing
// logs, and the person who cannot read it is not the person who chose it. The
// tokens already carry sentences about ratios -- "each one passes AA as TEXT
// on both grounds" -- and nothing has ever checked one.
//
// It matters more now than it did. Those sentences were written for a
// parchment ground; the console is taking the site's dark identity, and a
// colour tuned against #f2f4ed has no reason to work against #141711.

var tokenLine = regexp.MustCompile(`--color-([a-z0-9-]+):\s*([^;]+);`)

// tokens reads the `:root` block of the stylesheet SOURCE, which is where a
// human edits and therefore where a mistake is made. One level of `var()` is
// resolved, which is as deep as this file goes.
func tokens(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("app.src.css")
	if err != nil {
		t.Fatalf("the stylesheet moved: %v", err)
	}
	out := map[string]string{}
	for _, m := range tokenLine.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	for name, v := range out {
		if ref := strings.TrimSuffix(strings.TrimPrefix(v, "var(--color-"), ")"); ref != v {
			out[name] = out[ref]
		}
	}
	return out
}

// rgb parses #rgb, #rrggbb and #rrggbbaa. An alpha channel is DROPPED rather
// than blended: a wash is a background and never text, and a test that
// guessed at the blend would be a test about its own arithmetic.
func rgb(t *testing.T, name, v string) (r, g, b float64, ok bool) {
	t.Helper()
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "#") {
		return 0, 0, 0, false
	}
	h := v[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 && len(h) != 8 {
		t.Fatalf("--color-%s is %q, which this cannot read", name, v)
	}
	n, err := strconv.ParseUint(h[:6], 16, 32)
	if err != nil {
		t.Fatalf("--color-%s is %q: %v", name, v, err)
	}
	return float64(n >> 16 & 0xff), float64(n >> 8 & 0xff), float64(n & 0xff), true
}

// luminance is WCAG 2.1's relative luminance.
func luminance(r, g, b float64) float64 {
	f := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b)
}

func ratio(t *testing.T, tok map[string]string, fg, bg string) float64 {
	t.Helper()
	fr, fgg, fb, ok := rgb(t, fg, tok[fg])
	if !ok {
		t.Fatalf("--color-%s is not a hex colour: %q", fg, tok[fg])
	}
	br, bgg, bb, ok := rgb(t, bg, tok[bg])
	if !ok {
		t.Fatalf("--color-%s is not a hex colour: %q", bg, tok[bg])
	}
	l1, l2 := luminance(fr, fgg, fb), luminance(br, bgg, bb)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// EVERY TEXT ROLE ON EVERY GROUND IT IS DRAWN ON.
//
// 4.5:1 is WCAG AA for body text, and this interface is body text: a table of
// run names and timestamps, read for minutes at a time. 3:1 is the large-text
// allowance and is not claimed here, because almost nothing on these screens
// is large.
func TestEveryTextRoleIsReadableOnItsGround(t *testing.T) {
	tok := tokens(t)
	const aa = 4.5

	for _, c := range []struct{ fg, bg string }{
		{"ink", "canvas"},
		{"ink", "surface"},
		{"ink", "surface-2"},
		{"muted", "canvas"},
		{"muted", "surface"},
		{"muted", "surface-2"},
		// The accent is a link and a label, which are text.
		{"accent-strong", "canvas"},
		{"accent-strong", "surface"},
	} {
		if got := ratio(t, tok, c.fg, c.bg); got < aa {
			t.Errorf("%s on %s is %.2f:1, and body text needs %.1f:1 (%s / %s)",
				c.fg, c.bg, got, aa, tok[c.fg], tok[c.bg])
		}
	}
}

// THE STATE COLOURS ARE TEXT TOO, which their own comment says: "they are
// used as text on a pill and not only as a dot". A dot would need 3:1; a word
// needs 4.5:1, and the word is what is there.
func TestEveryStateColourIsReadableAsAWord(t *testing.T) {
	tok := tokens(t)
	const aa = 4.5

	states := []string{}
	for name := range tok {
		if strings.HasPrefix(name, "state-") {
			states = append(states, name)
		}
	}
	if len(states) < 5 {
		t.Fatalf("found %d state colours, which is not the set this is about", len(states))
	}
	for _, s := range states {
		for _, bg := range []string{"canvas", "surface"} {
			if got := ratio(t, tok, s, bg); got < aa {
				t.Errorf("%s on %s is %.2f:1, and it is drawn as a word (%s)",
					s, bg, got, tok[s])
			}
		}
	}
}

// A REPORT RATHER THAN A VERDICT, for whoever is changing the palette: every
// pair the tests above check, printed with its ratio.
func TestContrastReport(t *testing.T) {
	tok := tokens(t)
	for _, bg := range []string{"canvas", "surface", "surface-2"} {
		for _, fg := range []string{"ink", "muted", "accent-strong"} {
			t.Logf("%-14s on %-10s %.2f:1", fg, bg, ratio(t, tok, fg, bg))
		}
	}
}
