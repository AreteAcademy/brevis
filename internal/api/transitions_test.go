package api_test

import (
	"regexp"
	"testing"
)

// EVERY NAVIGATION CROSSFADES, AND THE SHEET IS WHERE THAT IS DECIDED.
//
// Each click on this console is a full document load: the server draws the
// whole page and the browser throws the old one away. Without the opt-in, the
// browser paints the new document from nothing, and the bar and the header
// blink on every section change. With it, the old frame is held until the new
// one can paint, and the two are crossfaded.
//
// The opt-in is a rule in the served sheet and not a script, so it costs
// nothing where it is unsupported: a browser without cross-document view
// transitions navigates the way it always has.
func TestEveryNavigationOptsIntoAViewTransition(t *testing.T) {
	_, ui := browsing(t)
	_, css := get(t, ui, "/assets/app.css")

	if !regexp.MustCompile(`@view-transition\s*\{\s*navigation\s*:\s*auto\s*;?\s*\}`).MatchString(css) {
		t.Error("the served sheet does not opt navigations into a view transition, so every section change repaints from nothing")
	}
}
