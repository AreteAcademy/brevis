package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
)

// TABS ARE AN ENHANCEMENT, SO THEY ARRIVE HIDDEN.
//
// The same rule the pager follows: a strip of controls that a browser
// without the island cannot drive is a strip of controls that does nothing,
// and the screen underneath already works -- one textarea, one statement.
// The island reveals it.
//
// THE MARKUP IS IN A <template> AND NOT IN THE SCRIPT, which is W1's rule
// read forward: two pieces of markup for one thing is how the classes and
// the ARIA drift apart. The island clones; it writes no tags.
func TestTheTabStripWaitsForTheIsland(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	strip := tag(body, "data-tabs")
	if strip == "" {
		t.Fatal("there is no tab strip on the screen")
	}
	if !strings.Contains(strip, "hidden") {
		t.Errorf("the tab strip is drawn for a browser that cannot drive it: %s", strip)
	}
	if !strings.Contains(body, "<template data-tab-template") {
		t.Error("the island has no tab markup to clone, so it would have to write its own")
	}
	if !strings.Contains(body, `role="tab"`) {
		t.Error("the cloned tab is not a tab to anything that reads the page aloud")
	}
}

// THE RECENT RAIL IS THE SAME, and it is emptier still without the island:
// what it lists lives in `localStorage`, so a browser with no script has
// nothing to put in it.
func TestTheRecentRailWaitsForTheIsland(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	rail := tag(body, "data-recent-keep")
	if rail == "" {
		t.Fatal("there is no Recent rail on the screen")
	}
	if !strings.Contains(rail, "hidden") {
		t.Errorf("an empty rail is drawn for a browser that can never fill it: %s", rail)
	}
	// THE CAP COMES FROM THE MARKUP, like the grips' `data-min`. A number
	// that lived only in the island is a number the next person changes
	// without the page saying what it is.
	if !strings.Contains(rail, `data-recent-keep="20"`) {
		t.Errorf("the rail does not say how much it keeps: %s", rail)
	}
	if !strings.Contains(body, "<template data-recent-template") {
		t.Error("the island has no entry markup to clone")
	}
	for _, slot := range []string{"data-recent-outcome", "data-recent-cost", "data-recent-text"} {
		if !strings.Contains(body, slot) {
			t.Errorf("an entry has nowhere to put %s", slot)
		}
	}
	// AND A WAY BACK THAT IS NOT INSIDE WHAT IT HIDES. W2 learned this on
	// the object tree: a collapsed rail is `display: none`, so a control
	// that lived in it goes with it.
	if !strings.Contains(body, "workbench-recent-show") {
		t.Error("collapsing the Recent rail would leave no way to open it")
	}
}

// WHAT HAPPENED IS ON THE PAGE AS DATA, not only as a sentence.
//
// The rail lists an outcome and what the statement cost. Both are already
// under the grid, for a person -- `Note()` writes "1 row · 2.0 KB scanned ·
// 41 ms". Parsing that sentence back out in JavaScript would break the day
// somebody rewords it, and formatting the bytes again in the island would be
// a SECOND formatter for one rule: `bytesText` rounds, and two rounders
// disagree eventually.
//
// So the server hands over the words it already wrote, and the island only
// stores and prints them.
func TestWhatARunCostIsOnThePageAsData(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"]],"bytes":2048,"ms":41}`
	body := run(t, ui, probeTarget, "SELECT 1")

	ran := tag(body, "data-outcome")
	if ran == "" {
		t.Fatal("nothing on the page says a query ran")
	}
	if !strings.Contains(ran, `data-outcome="ok"`) {
		t.Errorf("the run does not say it succeeded: %s", ran)
	}
	cost := attr(ran, "data-cost")
	if cost == "" {
		t.Fatalf("the run carries no cost: %s", ran)
	}
	// THE SAME WORDS AS THE SENTENCE UNDER THE GRID, because they come from
	// the same function. A rail that said "2 KB" under a grid that said
	// "2.0 KB" is a rail somebody stops trusting.
	said := textOf(body, "data-note")
	if said == "" {
		t.Fatal("there is no sentence under the grid to compare with")
	}
	if !strings.Contains(said, cost) || !strings.Contains(cost, "41 ms") {
		t.Errorf("the rail would print %q while the screen says %q", cost, said)
	}

	// AND A REFUSAL IS AN OUTCOME. A rail that only listed what succeeded
	// would be a rail somebody uses to conclude a query was never run.
	w.status["/v1/query"] = http.StatusBadRequest
	w.query = `{"error":"this service runs SELECT only"}`
	failed := tag(run(t, ui, probeTarget, "DROP TABLE t"), "data-outcome")
	if !strings.Contains(failed, `data-outcome="failed"`) {
		t.Errorf("a refused query is not recorded as refused: %s", failed)
	}
}

// NO TAB STATE REACHES A URL.
//
// Console 2.0's rule, and the reason is unchanged: a statement in a link is
// a statement in a proxy log, in a browser's history and in a Referer
// header. Tabs are the obvious thing to put in a query string -- it is how
// every other editor shares one -- so the refusal is written down where a
// change to it fails.
func TestNoTabStateReachesAURL(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)

	for _, forbidden := range []string{
		"pushState", "replaceState", "location.search", "location.hash", "URLSearchParams",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("the island uses %s: tab state in an address bar is a statement in a history file", forbidden)
		}
	}

	_, body := get(t, ui, "/sql")
	if strings.Contains(body, `name="tab"`) {
		t.Error("the form carries a tab field, which a GET would put in the URL")
	}

	// AND THE SERVER IGNORES ONE ANYWAY.
	_, with := get(t, ui, "/sql?tab=2&target="+url.QueryEscape(probeTarget))
	if strings.Contains(with, `value="2"`) {
		t.Error("a tab index in the URL reached the page")
	}
}

// BOTH LISTS LIVE IN THE BROWSER, and the island says which keys hold them.
//
// Console 2.0 drew this boundary -- "saved queries live in the BROWSER until
// there is a user model" -- and the cost is real: the statements are on that
// machine, in that browser, and a second browser starts empty. A server
// store would need a user model, and without one it is a shared mutable list
// with no owner.
func TestTabsAndRecentAreKeptInTheBrowser(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)

	for _, want := range []string{`"tabs"`, `"recent"`, "data-recent-keep"} {
		if !strings.Contains(js, want) {
			t.Errorf("the island's CODE never uses %q", want)
		}
	}
	// AND NOTHING SENDS THEM ANYWHERE. Three destinations and no more: two
	// fragments that carry a target and a relation, and the pricing one
	// that carries THE statement in the box -- one, the one about to run,
	// never the list. A fourth would be everything somebody typed leaving
	// the machine.
	for _, path := range endpoints(js) {
		switch path {
		case "/api/sql/objects", "/api/sql/columns", "/api/sql/estimate":
		default:
			t.Errorf("the island talks to %q, which is not one of its three endpoints", path)
		}
	}
}

// run submits the editor's form the way the Run button does.
func run(t *testing.T, ui *api.UI, target, statement string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"target": {target}, "q": {statement}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Body.String()
}

// tag is the one element carrying an attribute, so an assertion about "the
// rail" cannot be satisfied by a word somewhere else in the document.
func tag(html, attr string) string {
	at := strings.Index(html, attr)
	if at < 0 {
		return ""
	}
	start := strings.LastIndex(html[:at], "<")
	end := strings.Index(html[at:], ">")
	if start < 0 || end < 0 {
		return ""
	}
	return html[start : at+end+1]
}

// attr reads one attribute out of an element, unescaping what templ wrote.
func attr(el, name string) string {
	at := strings.Index(el, name+`="`)
	if at < 0 {
		return ""
	}
	rest := el[at+len(name)+2:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return strings.ReplaceAll(rest[:end], "&#43;", "+")
}

// textOf is what one marked element says, which is not the same question as
// "does this string appear in the document": the document also holds the
// attribute the island reads, and it is the same string.
func textOf(html, attr string) string {
	el := tag(html, attr)
	if el == "" {
		return ""
	}
	rest := html[strings.Index(html, el)+len(el):]
	end := strings.Index(rest, "<")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// endpoints is every path the island fetches, read out of its code.
func endpoints(js string) []string {
	var out []string
	for _, part := range strings.Split(js, `"/api/`)[1:] {
		if end := strings.IndexAny(part, `"?`); end >= 0 {
			out = append(out, "/api/"+part[:end])
		}
	}
	return out
}
