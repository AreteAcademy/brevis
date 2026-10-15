package assets

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// THE ROLES THE REDESIGN ADDS ARE PAINTED BY THE BRAND, NOT BESIDE IT.
//
// An installation's brand.yaml repaints the console through the variables
// internal/branding writes, and nothing else. A new role written as a literal
// would be the one part of the screen a client theme cannot reach: the
// sidebar's ground or a table's metadata left in this palette on somebody
// else's. So each one is a mix of variables, and every variable it mixes
// resolves to one branding.go writes.
//
// The write list is read from branding.go rather than copied here, so the day
// that list changes this is checked against the new one.
func TestEveryDerivedRoleIsPaintedByTheBrand(t *testing.T) {
	raw, err := os.ReadFile("../../internal/branding/branding.go")
	if err != nil {
		t.Fatalf("branding.go moved: %v", err)
	}
	branded := map[string]bool{}
	for _, m := range regexp.MustCompile(`write\("--color-([a-z0-9-]+)"`).FindAllStringSubmatch(string(raw), -1) {
		branded[m[1]] = true
	}
	if len(branded) < 10 {
		t.Fatalf("found %d branded variables, which is not the write list this is about", len(branded))
	}

	src := sourceTokens(t)
	refs := regexp.MustCompile(`var\(--color-([a-z0-9-]+)\)`)
	for _, role := range []string{"faint", "ink-2", "surface-3", "canvas-subtle"} {
		v, ok := src[role]
		if !ok {
			t.Errorf("--color-%s is not defined", role)
			continue
		}
		if strings.Contains(v, "#") {
			t.Errorf("--color-%s carries a literal colour (%s), which no brand can repaint", role, v)
		}
		found := refs.FindAllStringSubmatch(v, -1)
		if len(found) == 0 {
			t.Errorf("--color-%s mixes no variable at all (%s)", role, v)
		}
		for _, f := range found {
			if !branded[root(src, f[1])] {
				t.Errorf("--color-%s mixes --color-%s, which resolves to nothing branding.go writes", role, f[1])
			}
		}
	}
}

// sourceTokens is every `--color-*` as WRITTEN, before tokens() resolves it.
func sourceTokens(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("app.src.css")
	if err != nil {
		t.Fatalf("the stylesheet moved: %v", err)
	}
	out := map[string]string{}
	for _, m := range tokenLine.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

// root follows a chain of plain aliases (`--color-canvas: var(--color-parchment)`)
// to the name at its end.
func root(src map[string]string, name string) string {
	for i := 0; i < 8; i++ {
		v := src[name]
		next := strings.TrimSuffix(strings.TrimPrefix(v, "var(--color-"), ")")
		if next == v {
			return name
		}
		name = next
	}
	return name
}
