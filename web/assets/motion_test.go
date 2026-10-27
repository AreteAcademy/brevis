package assets

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// EVERYTHING THAT MOVES STANDS STILL FOR WHOEVER ASKED.
//
// `prefers-reduced-motion: reduce` is a person saying motion makes them ill
// or slows them down. A pulse, a spinner, a sliding drawer or a crossfade
// that ignores it is the console overruling them. So every rule that
// animates names its selector again inside a reduce block -- the shape that
// carried the meaning (a spinner's ring with its gap, a live dot) stays, and
// only the motion goes.
func TestEveryAnimationStopsForReducedMotion(t *testing.T) {
	raw, err := os.ReadFile("app.src.css")
	if err != nil {
		t.Fatal(err)
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")

	reduce := regexp.MustCompile(`@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{`)
	var still strings.Builder
	for _, at := range reduce.FindAllStringIndex(css, -1) {
		depth, i := 1, at[1]
		for ; i < len(css) && depth > 0; i++ {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		still.WriteString(css[at[1]:i])
	}
	stopped := still.String()

	rule := regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		body := m[2]
		if !regexp.MustCompile(`(?m)^\s*animation:\s*[a-z]`).MatchString(body) || strings.Contains(body, "animation: none") {
			continue
		}
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			if sel == "" || strings.HasPrefix(sel, "@") || strings.Contains(sel, "%") || sel == "from" || sel == "to" {
				continue
			}
			// A view-transition pseudo is stopped for every name by `(*)`.
			wildcard := regexp.MustCompile(`\(([a-z-]+)\)$`).ReplaceAllString(sel, "(*)")
			if !strings.Contains(stopped, sel) && !strings.Contains(stopped, wildcard) {
				t.Errorf("%s animates and no reduced-motion block stops it", sel)
			}
		}
	}
}
