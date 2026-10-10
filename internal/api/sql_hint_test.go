package api_test

import (
	"net/url"
	"strings"
	"testing"
)

// THE COMPLETION LIST IS THE TREE, so the tree has to carry the names in a
// form a script can read.
//
// The relation's own name was already there -- `data-relation` -- because the
// filter reads it. Its columns were text in two spans, which is a list a
// script reads by guessing which span is the name. `data-column` makes that
// an agreement instead of a guess, and it is the same lesson `data-insert`
// taught one slice ago: a name referenced and a name honoured are different
// facts.
func TestTheTreeCarriesTheNamesTheCompletionReads(t *testing.T) {
	_, ui := browsing(t)
	body := expand(t, ui, probeTarget, "", "", "sales.daily")

	if !strings.Contains(body, `data-relation="sales.daily"`) {
		t.Error("the relation is not marked, so no completion can pair it with its columns")
	}
	for _, want := range []string{`data-column="sku"`, `data-column="quantity"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the open relation does not carry %s", want)
		}
	}

	// AND THE FRAGMENT TOO, which is where almost every column actually
	// arrives: with the island running, opening a relation never reloads
	// the page, so the markup above is the path nobody takes.
	_, frag := get(t, ui, "/api/sql/columns?target="+url.QueryEscape(probeTarget)+"&schema=sales&name=daily")
	if !strings.Contains(frag, `data-column="sku"`) {
		t.Errorf("the fragment carries no readable column names:\n%s", frag)
	}
}

// THE ISLAND COMPLETES FROM THE TREE AND NOT FROM A LIST IT KEEPS.
//
// STRIPPED OF ITS COMMENTS, for the reason the tree's own island test is:
// every string below is also in the paragraph that explains it, so the
// unstripped version of this test passes against a file that completes
// nothing.
func TestTheIslandCompletesFromTheTree(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)

	for _, want := range []string{
		"Ctrl-Space",          // the key the slice promises
		"showHint",            // what opens the list
		"CodeMirror.hint.sql", // and what fills it
		`"data-relation"`,     // the relations it offers
		`"data-column"`,       // and the columns of the ones that are open
		"inputRead",           // the `.` that opens it without the key
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the island's CODE never uses %q, so that half of the completion does not exist", want)
		}
	}
}

// AND THE LIST IS READABLE ON THIS PALETTE.
//
// show-hint ships `background:#fff` and `color:#000`. The console is dark --
// `--color-parchment` is `#141711` -- so the vendored default is a white box
// in the middle of a dark editor, and its selected row is a blue nobody here
// chose. A rule written and a rule SHIPPED are different facts, so this reads
// the sheet that is served.
func TestTheCompletionListIsDrawnInThisPalette(t *testing.T) {
	_, ui := browsing(t)
	_, css := get(t, ui, "/assets/app.css")

	for _, want := range []string{".CodeMirror-hints", ".CodeMirror-hint"} {
		if !strings.Contains(css, want) {
			t.Errorf("the served sheet never mentions %s: the completion list keeps the vendor's white box", want)
		}
	}
}

// THE SCREEN SAYS WHICH HALF OF THE WAREHOUSE IT KNOWS, ONCE.
//
// Columns are fetched per relation, because a project-wide COLUMNS query is
// the one metadata answer that is genuinely large -- CHECKPOINT D refused it.
// So the completion knows the columns of what somebody opened and nothing
// else, and a list that silently knows half the warehouse is how somebody
// concludes a column does not exist.
func TestTheEditorSaysWhichHalfItCanComplete(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	const said = "Columns complete for relations you have opened"
	if n := strings.Count(body, said); n != 1 {
		t.Errorf("the limitation is on the screen %d times, and it is stated exactly once", n)
	}
}
