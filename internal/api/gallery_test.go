package api_test

import (
	"github.com/AreteAcademy/brevis/web/components"
	"net/http"
	"strings"
	"testing"
)

// THE GALLERY EXISTS ONLY WHERE SOMEBODY IS BUILDING THE CONSOLE.
//
// It is every component with sample data -- what a screen is assembled from,
// in one place to look at. On an installation it is a page nobody asked for
// and one more route to reason about, so it is not registered at all: a 404,
// not a page that says "not here".
func TestTheComponentGalleryIsNotServedByDefault(t *testing.T) {
	code, _ := get(t, dataUI(catalogFake{}), "/_components")
	if code != http.StatusNotFound {
		t.Errorf("the gallery answered %d on a console that did not ask for it", code)
	}
}

// AND WHERE IT IS SERVED, IT SHOWS EVERY COMPONENT. A gallery missing one is
// how a component drifts unseen until a screen uses it.
func TestTheComponentGalleryShowsEveryComponent(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{}).WithGallery(true), "/_components")
	if code != http.StatusOK {
		t.Fatalf("the gallery answered %d", code)
	}
	for what, marker := range map[string]string{
		"Button":          `type="button"`,
		"IconButton":      `aria-label="Copy URI"`,
		"Kbd":             "<kbd",
		"SearchInput":     `type="search"`,
		"Segmented":       `aria-current="true"`,
		"Tabs":            `role="tablist"`,
		"Stats":           "<dl",
		"MethodPill":      ">GET<",
		"TableFoot":       "Showing 11 of 15",
		"DefinitionList":  "<dt",
		"CodeBlock":       "data-copy",
		"State":           "never ran",
		"Freshness":       "continuous",
		"Empty":           "Nothing here yet",
		"Icon (database)": ">database<",
		"Icon (terminal)": ">terminal<",
		"Drawer":          `<dialog id="gallery-drawer" class="drawer"`,
		"Modal":           `<dialog id="gallery-modal" class="modal"`,
		"Toast":           `class="toast"`,
		"Switch":          `class="switch"`,
		"NavBadge (fail)": "nav-badge-fail",
		"NavBadge (live)": "nav-badge-live",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("the gallery does not show %s (no %s)", what, marker)
		}
	}
	for _, k := range components.StatusKinds() {
		if !strings.Contains(body, `data-gallery-status="`+k+`"`) {
			t.Errorf("the gallery does not show the state %s", k)
		}
	}
	for _, k := range components.LogoKeys() {
		if !strings.Contains(body, `data-gallery-logo="`+k+`"`) {
			t.Errorf("the gallery does not show the logo %s", k)
		}
	}
	if !strings.Contains(body, `role="img"`) {
		t.Error("the gallery does not show a StatusMark")
	}
}
