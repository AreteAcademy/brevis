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

// THE ROLES THE 2.0 DESIGN NAMES EXIST, AND EVERY ONE IS STILL THE BRAND'S.
//
// The handoff speaks in roles -- bg, fg, subtle, border, ok, fail -- and the
// screens built from it will too. They arrive as aliases and mixes over the
// names branding.go writes, never as values, for the reason the test above
// gives. Unlike that test, this one follows a role THROUGH other roles
// (`subtle` is `faint`, which is a mix) until every leaf is a branded name, so
// an alias of a literal cannot pass for an alias of the brand.
//
// `accent` is not on the list. The repository's `--color-accent` is the dim
// gold a long-standing set of call sites paints with; the handoff's accent --
// the one strong colour on a screen -- is what this file calls
// `accent-strong`, and repainting `accent` to match a name would repaint every
// screen at once.
func TestTheDesignRolesArePaintedByTheBrand(t *testing.T) {
	raw, err := os.ReadFile("../../internal/branding/branding.go")
	if err != nil {
		t.Fatalf("branding.go moved: %v", err)
	}
	branded := map[string]bool{}
	for _, m := range regexp.MustCompile(`write\("--color-([a-z0-9-]+)"`).FindAllStringSubmatch(string(raw), -1) {
		branded[m[1]] = true
	}

	src := sourceTokens(t)
	refs := regexp.MustCompile(`var\(--color-([a-z0-9-]+)\)`)
	var leaves func(name string, depth int) []string
	leaves = func(name string, depth int) []string {
		if branded[name] || depth > 8 {
			return []string{name}
		}
		v, ok := src[name]
		if !ok {
			return []string{name}
		}
		found := refs.FindAllStringSubmatch(v, -1)
		if len(found) == 0 {
			return []string{name}
		}
		var out []string
		for _, f := range found {
			out = append(out, leaves(f[1], depth+1)...)
		}
		return out
	}

	for _, role := range []string{
		"bg", "fg", "subtle", "border", "border-strong", "hover", "bar",
		"accent-soft", "on-accent", "ok", "fail", "warn", "run",
	} {
		v, ok := src[role]
		if !ok {
			t.Errorf("--color-%s is not defined", role)
			continue
		}
		if strings.Contains(v, "#") {
			t.Errorf("--color-%s carries a literal colour (%s), which no brand can repaint", role, v)
			continue
		}
		for _, leaf := range leaves(role, 0) {
			if !branded[leaf] {
				t.Errorf("--color-%s ends in --color-%s, which is nothing branding.go writes", role, leaf)
			}
		}
	}
}
