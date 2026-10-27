package api_test

import (
	"strings"
	"testing"
)

// `g` THEN A LETTER GOES TO A SECTION, and the letter is written on the
// section's own link. The script reads it there, so there is no second map to
// fall out of step with the sidebar -- a section that is not drawn (SQL with
// no service) has no letter to press.
func TestEachSectionCarriesItsGoKey(t *testing.T) {
	_, ui := browsing(t)
	body := sections(render(t, ui, "/ingest/loads"))
	for href, key := range map[string]string{
		"/": "o", "/workflows": "w", "/runs": "e", "/sql": "s", "/ingest/streams": "t",
	} {
		at := strings.Index(body, `href="`+href+`"`)
		if at < 0 {
			t.Errorf("no section links %s", href)
			continue
		}
		tag := body[at : at+strings.Index(body[at:], ">")]
		if !strings.Contains(tag, `data-go-key="`+key+`"`) {
			t.Errorf("%s does not carry g %s: %s", href, key, tag)
		}
	}
	if !strings.Contains(sections(render(t, dataUI(catalogFake{}), "/ingest/loads")), `href="/runs"`) {
		t.Fatal("a console without SQL lost its sections")
	}
	if strings.Contains(render(t, dataUI(catalogFake{}), "/ingest/loads"), `data-go-key="s"`) {
		t.Error("g s is offered on a console with no SQL section")
	}
}

// `?` LISTS THE SHORTCUTS, in a modal every page carries, opened from the
// sidebar's foot as well -- a shortcut nobody can discover is not one.
func TestTheShortcutsAreListedInAModal(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")
	at := strings.Index(body, `<dialog id="shortcuts" class="modal"`)
	if at < 0 {
		t.Fatal("the page has no shortcuts modal")
	}
	modal := body[at : at+strings.Index(body[at:], "</dialog>")]
	for _, want := range []string{"⌘K", ">/<", ">?<", "Esc", ">g<", ">o<", "Overview", ">w<", "Workflows"} {
		if !strings.Contains(modal, want) {
			t.Errorf("the shortcuts modal does not list %s", want)
		}
	}
	aside, end := strings.Index(body, "<aside"), strings.Index(body, "</aside>")
	o := strings.Index(body, `data-dialogo="shortcuts"`)
	if o < aside || o > end {
		t.Error("the sidebar has no way to open the shortcuts")
	}
}

// THE SCRIPT HALF. A letter typed into a field is a letter, never a jump:
// the guard is the one the `/` shortcut already uses.
func TestTheShortcutScriptWaitsForAKeyOutsideAField(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{`[data-go-key="`, `e.key === "?"`, `typing(e.target)`, `"toggle-theme"`} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js never uses %s", want)
		}
	}
	// Two handlers: `/`, and one for `g` and `?` together -- each asks
	// before it acts, so `?` must come after its handler's guard.
	if n := strings.Count(js, "typing(e.target)"); n < 2 {
		t.Errorf("only %d handlers check for a field being typed in; the / one and the g/? one must", n)
	}
	q := strings.Index(js, `e.key === "?"`)
	if guard := strings.LastIndex(js[:max(q, 0)], "typing(e.target)"); q < 0 || guard < 0 || q-guard > 400 {
		t.Error("the ? shortcut is not behind the typing guard")
	}
}
