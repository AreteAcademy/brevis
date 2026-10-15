package api_test

import (
	"strings"
	"testing"
)

// THE ANSWER IS WIRED FROM ITS OWN NODE, NOT FROM THE DOCUMENT.
//
// A run is about to replace the answer pane without reloading the
// workbench. Whatever the island reads about that answer -- the pager, the
// rows it pages over, the outcome Recent records -- has to come from the node
// that was just drawn. Read from `document`, the island wires the answer the
// page LOADED with, once, and a second answer arrives with a pager nobody
// drives and a run Recent never records.
//
// So the pane carries a marker, and the island reads those three through a
// root it is handed. A tab click is delegated once and finds its own pane
// from the tab, so it needs no second listener per answer.
func TestTheAnswerIsWiredFromItsOwnNode(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"]]}`
	if body := ran(t, ui, "SELECT 1"); !strings.Contains(body, "data-answer") {
		t.Error("the answer pane carries no marker, so there is no node to wire it from")
	}

	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)
	for _, want := range []string{
		`root.querySelector("[data-pager]")`,
		`root.querySelector("[data-outcome]")`,
		`root.querySelectorAll("[data-panel='results'] [data-row]")`,
		`closest("[data-answer]")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the island does not read %s from the answer it is handed", want)
		}
	}
	for _, gone := range []string{
		`document.querySelector("[data-pager]")`,
		`document.querySelector("[data-outcome]")`,
		`document.querySelectorAll("[data-panel='results'] [data-row]")`,
	} {
		if strings.Contains(js, gone) {
			t.Errorf("the island still reads %s from the document, which is the answer the page loaded with", gone)
		}
	}
}
