package api_test

import (
	"strings"
	"testing"
)

// THE THREE ZONES, IN ORDER.
//
// Position and not presence. A workbench is an explorer, an editor and the
// answer, and "all three are somewhere in the document" is what seven tests
// in this repository once asserted about a panel that was rendering outside
// the page.
func TestTheWorkbenchHasThreeZonesInOrder(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	rail := strings.Index(body, `data-rail`)
	editor := strings.Index(body, `data-pane="editor"`)
	results := strings.Index(body, `data-pane="results"`)

	switch {
	case rail < 0:
		t.Fatal("there is no explorer rail")
	case editor < 0:
		t.Fatal("there is no editor zone")
	case results < 0:
		t.Fatal("there is no results zone")
	}
	if rail >= editor || editor >= results {
		t.Errorf("the zones are out of order: rail=%d editor=%d results=%d", rail, editor, results)
	}
}

// THE GRIPS DECLARE THEIR MINIMUM IN THE MARKUP.
//
// A DRAG IS NOT SIMULATED HERE, and this test does not pretend otherwise.
// What it refuses is the version where the floor lives only inside the
// script: a minimum nobody can read from the page is a minimum the next
// person deletes without noticing, and the failure -- a pane dragged to
// nothing, with no way back -- happens to somebody else.
func TestTheGripsDeclareTheirMinimum(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	for _, want := range []string{
		`data-grip="rail"`,
		`data-grip="split"`,
		`role="separator"`,
		`aria-orientation="vertical"`,
		`aria-orientation="horizontal"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the chrome does not carry %q", want)
		}
	}
	// Each grip says its own floor, and neither says zero.
	for _, grip := range []string{`data-grip="rail"`, `data-grip="split"`} {
		at := strings.Index(body, grip)
		if at < 0 {
			continue
		}
		// The attributes of one element, not of the whole document.
		el := body[at:]
		if end := strings.Index(el, ">"); end > 0 {
			el = el[:end]
		}
		if !strings.Contains(el, "data-min=") {
			t.Errorf("%s declares no minimum: %s", grip, el)
		}
		if strings.Contains(el, `data-min="0"`) {
			t.Errorf("%s declares a minimum of nothing", grip)
		}
	}
}

// RUN LOOKS LIKE THE THING YOU PRESS, and says the key that does it.
//
// `Ctrl-Enter` and `Cmd-Enter` have run the query since the editor island
// landed, and the screen has never said so -- a shortcut nobody is told
// about is a shortcut nobody uses.
func TestRunIsThePrimaryActionAndSaysItsShortcut(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	at := strings.Index(body, `data-run`)
	if at < 0 {
		t.Fatal("the Run button is not marked as the primary action")
	}
	el := body[at:]
	if end := strings.Index(el, ">"); end > 0 {
		el = el[:end]
	}
	if !strings.Contains(el, "bg-accent") {
		t.Errorf("Run does not carry the accent that makes it primary: %s", el)
	}
	if !strings.Contains(body, "⌘↵") {
		t.Error("the screen never says which key runs the query")
	}
}

// THE RAIL COLLAPSES, and the control says what it does to a screen reader
// as well as to an eye.
func TestTheRailCanBeCollapsed(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	at := strings.Index(body, `data-rail-toggle`)
	if at < 0 {
		t.Fatal("the explorer cannot be collapsed")
	}
	el := body[at:]
	if end := strings.Index(el, ">"); end > 0 {
		el = el[:end]
	}
	for _, want := range []string{"aria-expanded", "aria-controls", "aria-label"} {
		if !strings.Contains(el, want) {
			t.Errorf("the collapse control has no %s: %s", want, el)
		}
	}
}

// THE ISLAND AND THE CHROME NAME THE SAME THINGS.
//
// Two files have to agree on five strings, and nothing but this would
// notice when one of them is renamed. It is the same class of agreement as
// the tree's attributes, and the same reason: four tests once agreed the
// page referenced `/assets/sql.js` while it 404'd.
func TestTheIslandAndTheChromeAgreeOnTheMarkers(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")

	// THE COMMENTS ARE STRIPPED FIRST, and that is not tidiness.
	//
	// This test passed a mutation. Replacing `grip.getAttribute("data-min")`
	// with `null` -- which is the island forgetting the floor entirely --
	// left the assertion green, because the paragraph above that line says
	// the words "data-min". The test was reading the prose that explains the
	// code instead of the code.
	//
	// A file that only TALKS about an attribute does not read it.
	js := strip(raw)

	for _, want := range []string{
		"data-grip", "data-min", "data-rail-toggle", "--rail", "--editor",
		"localStorage",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the island's CODE never uses %q, so that part of the chrome is inert", want)
		}
	}
}

// strip removes `//` comments, so an assertion about what a script DOES
// cannot be satisfied by what it says about itself.
//
// Line comments only, and that is enough here: this file has no block
// comments, and a stripper that tried to be a parser would be a second
// JavaScript implementation nobody asked for. A string containing `//` --
// a URL -- would be cut short, which costs this test nothing, because the
// paths it looks for are the leading part.
func strip(src string) string {
	var out strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if at := strings.Index(line, "//"); at >= 0 {
			line = line[:at]
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// THE WORKBENCH FILLS THE SHELL INSTEAD OF SCROLLING IT, and the rule that
// makes that true is in the SHEET THAT IS SERVED.
//
// Writing a rule and shipping one are different facts -- the same distinction
// that `/assets/sql.js` taught by 404ing while four tests agreed the page
// referenced it. A class in a template with no rule behind it is a layout
// that silently does nothing.
func TestTheWorkbenchFillsTheShell(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")
	if !strings.Contains(body, `data-fill`) {
		t.Error("the workbench does not ask the shell for the whole viewport")
	}

	_, css := get(t, ui, "/assets/app.css")
	for _, want := range []string{"main[data-fill]", "--rail", "--editor"} {
		if !strings.Contains(css, want) {
			t.Errorf("the stylesheet that is served carries no %q", want)
		}
	}

	// That no OTHER screen asks for it is asserted where every screen can be
	// rendered at once: web/pages/width_test.go.
}
