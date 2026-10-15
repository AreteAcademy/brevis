package pages

import (
	"regexp"
	"strings"
	"testing"
)

// EVERY ASSET A PAGE ASKS FOR CARRIES ITS VERSION. One reference written by
// hand would be the one file revalidated on every click -- and nothing on the
// screen would say which.
func TestEveryAssetAPageAsksForIsVersioned(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="(/assets/[^"]*)"`)
	pages := append(screens(), screen{"login", Login(LoginData{})})
	for _, s := range pages {
		page := draw(t, s.c)
		found := ref.FindAllStringSubmatch(page, -1)
		if len(found) == 0 {
			t.Errorf("%s asks for no asset at all, so this test sees nothing", s.name)
		}
		for _, m := range found {
			if !strings.Contains(m[1], "?v=") {
				t.Errorf("%s asks for %s with no version", s.name, m[1])
			}
		}
	}
}
