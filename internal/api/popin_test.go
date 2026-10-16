package api_test

import (
	"os"

	"github.com/AreteAcademy/brevis/internal/api"
	"regexp"
	"strings"
	"testing"
)

// hiddenAttr is a bare `hidden` attribute in a tag.
var hiddenAttr = regexp.MustCompile(`\shidden(\s|>|=|/)`)

// tagAround is the opening tag that holds the byte at i, and the one before it
// -- an element, and the wrapper a script used to un-hide.
func tagAround(body string, i int) (own, parent string) {
	start := strings.LastIndex(body[:i], "<")
	own = body[start : start+strings.Index(body[start:], ">")+1]
	if p := strings.LastIndex(body[:start], "<"); p >= 0 {
		parent = body[p : p+strings.Index(body[p:], ">")+1]
	}
	return own, parent
}

// NOTHING POPS IN AFTER THE FIRST PAINT (nav-latency N6). What a page offers
// only with a script was drawn `hidden` and un-hidden by ui.js or sql.js, so a
// paint before the deferred script -- every navigation on real
// infrastructure, where the HTML arrives in pieces -- drew the page without
// it, and it popped in: the sidebar's search, shifting the whole nav down.
//
// Now each carries `data-js` and the CSS shows it under `<html class="js">`,
// which an inline head script sets before anything is drawn. No `hidden` on
// them or their wrapper, and no script reveals them.
func TestNothingScriptOnlyIsRevealedAfterThePaint(t *testing.T) {
	_, sql := browsing(t)
	stream := dataUI(catalogFake{entries: gatewayCatalog(), detail: nil})
	loads := dataUI(catalogFake{entries: demoCatalog()})
	load := loadUI(lateDetail(), nil)
	runs := runsUI(runsReads{})

	for _, c := range []struct {
		name, path, marker string
		ui                 *api.UI
	}{
		{"the ⌘K opener", "/ingest/loads", "data-palette-open", loads},
		{"a group toggle", "/ingest/loads", "data-group-toggle", loads},
		{"the Live switch", "/runs", `data-live-list="runs-list"`, runs},
		{"Copy URI", "/ingest/loads/orders_sync/load", `data-copy="#destination-uri"`, load},
		{"the stream's tab bar", "/ingest/streams/edge/v1/clicks", "data-hash-tabs-bar", stream},
		{"the workbench's tabs", "/sql", "data-tabs", sql},
		{"the recent rail", "/sql", `id="recent"`, sql},
		{"Copy statement", "/sql", "data-copy-statement", sql},
	} {
		_, body := get(t, c.ui, c.path)
		i := strings.Index(body, c.marker)
		if i < 0 {
			t.Errorf("%s: %s has no %s", c.name, c.path, c.marker)
			continue
		}
		own, parent := tagAround(body, i)
		if hiddenAttr.MatchString(own) || hiddenAttr.MatchString(parent) {
			t.Errorf("%s is drawn hidden, to be revealed after the paint: %s %s", c.name, parent, own)
		}
		if !strings.Contains(own, "data-js") && !strings.Contains(parent, "data-js") {
			t.Errorf("%s is not marked data-js: %s %s", c.name, parent, own)
		}
	}

	// The CSV export appears only with an answer; its markup is checked at
	// the source.
	src, err := os.ReadFile("../../web/pages/sql.templ")
	if err != nil {
		t.Fatal(err)
	}
	at := strings.Index(string(src), `"data-export-csv"`)
	wrapper := string(src)[strings.LastIndex(string(src)[:at], "<span"):at]
	if hiddenAttr.MatchString(wrapper) || !strings.Contains(wrapper, "data-js") {
		t.Errorf("the CSV export is not data-js: %s", wrapper)
	}

	// The head marks the page before the body is parsed, and the sheet shows
	// data-js only under it.
	_, page := get(t, loads, "/ingest/loads")
	head := page[strings.Index(page, "<head>"):strings.Index(page, "<title>")]
	if !strings.Contains(head, `classList.add("js")`) {
		t.Errorf("the head does not mark the page before it is drawn: %s", head)
	}
	_, css := get(t, loads, "/assets/app.css")
	if !strings.Contains(css, "[data-js]") {
		t.Error("the sheet has no rule for data-js")
	}

	// And the scripts reveal nothing at load.
	for path, gone := range map[string][]string{
		"/assets/ui.js":  {`parentElement.removeAttribute("hidden")`, "bar.hidden = false", "button.hidden = false", "button.parentElement.hidden = false"},
		"/assets/sql.js": {"strip.hidden = false", "recent.rail.hidden = false", `parentElement.removeAttribute("hidden")`},
	} {
		_, js := get(t, loads, path)
		for _, g := range gone {
			if strings.Contains(strip(js), g) {
				t.Errorf("%s still reveals at load: %s", path, g)
			}
		}
	}
}

// A LINK TO A STREAM'S #deadletter PAINTS DEAD LETTER FIRST. The head writes
// the hash on <html> before the body is drawn, and the page's own rules show
// the panel -- and mark the tab -- it names, Overview when it names none.
func TestAStreamsTabIsRightInTheFirstPaint(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: gatewayCatalog()}), "/ingest/streams/edge/v1/clicks")
	head := body[strings.Index(body, "<head>"):strings.Index(body, "<title>")]
	if !strings.Contains(head, "data-hash") && !strings.Contains(head, "dataset.hash") {
		t.Error("the head does not write the hash before the paint")
	}
	for _, want := range []string{
		`.js[data-hash="config"]`, `.js[data-hash="deadletter"]`, `[data-tab-panel="config"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the stream page's tab rules lack %s", want)
		}
	}
}

// THE PAGE TITLE IS 20px (18px IN MONO), the content 20px 24px (density-base
// DB1): the title was a 25.6px display face over a 24px top margin.
func TestThePageTitleIsCompact(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	h1 := body[strings.Index(body, "<h1"):]
	h1 = h1[:strings.Index(h1, ">")]
	if !strings.Contains(h1, "text-[20px]") || !strings.Contains(h1, "font-semibold") {
		t.Errorf("the title is not 20px/600: %s", h1)
	}
	_, mono := get(t, loadUI(lateDetail(), nil), "/ingest/loads/orders_sync/load")
	h1 = mono[strings.Index(mono, "<h1"):]
	h1 = h1[:strings.Index(h1, ">")]
	if !strings.Contains(h1, "text-[18px]") || !strings.Contains(h1, "font-mono") {
		t.Errorf("the mono title is not 18px: %s", h1)
	}
}
