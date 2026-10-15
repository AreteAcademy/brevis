package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/web/pages"
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

// submit posts the workbench's form, asking for the answer alone when
// fragment is set -- what the island does on Run.
func submit(t *testing.T, ui interface{ Registrar(*http.ServeMux) }, form url.Values, fragment bool) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if fragment {
		r.Header.Set("X-Brevis-Fragment", "answer")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func asked(calls []string, path string) bool {
	for _, c := range calls {
		if c == path {
			return true
		}
	}
	return false
}

// A RUN CAN ASK FOR THE ANSWER ALONE, AND THEN THE WAREHOUSE IS ASKED ONLY
// THE QUERY.
//
// The island replaces the answer pane and nothing else, so the tree and the
// open relation's columns -- two metadata round trips every full run pays --
// are work for a page nobody will draw. With the header, the response is the
// pane's markup and the service is asked one thing: the statement.
func TestARunCanAskForTheAnswerAlone(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"]]}`
	code, body := submit(t, ui, url.Values{"target": {probeTarget}, "q": {"SELECT 1"}, "open": {"sales.daily"}}, true)
	if code != http.StatusOK {
		t.Fatalf("/sql answered %d", code)
	}
	for _, want := range []string{"data-outcome", `data-tab="results"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer lacks %s", want)
		}
	}
	for _, gone := range []string{"<html", "<aside", "data-rail", `id="workbench"`} {
		if strings.Contains(body, gone) {
			t.Errorf("the answer carries %s, which is the page around it", gone)
		}
	}
	if asked(w.calls, "/v1/objects") || asked(w.calls, "/v1/columns") {
		t.Errorf("the warehouse was asked for metadata nobody will draw: %v", w.calls)
	}
	if len(w.calls) != 1 {
		t.Errorf("the warehouse was asked %d things for one statement: %v", len(w.calls), w.calls)
	}
}

// WITHOUT THE HEADER, A RUN IS WHAT IT ALWAYS WAS: the whole workbench, tree
// and all. That is the path with no script, and it does not change.
func TestWithoutTheHeaderARunDrawsTheWholeWorkbench(t *testing.T) {
	w, ui := browsing(t)
	code, body := submit(t, ui, url.Values{"target": {probeTarget}, "q": {"SELECT 1"}}, false)
	if code != http.StatusOK {
		t.Fatalf("/sql answered %d", code)
	}
	if !strings.Contains(body, "<html") || !strings.Contains(body, "data-answer") {
		t.Error("a run without the header no longer draws the workbench")
	}
	if !asked(w.calls, "/v1/objects") {
		t.Error("a full run no longer draws the tree")
	}
}

// THE REFUSALS DO NOT CHANGE WITH THE SHAPE. A body naming a connection the
// catalog does not know is refused in the pane, as on the full page; a
// statement past the ceiling is the same 400.
func TestTheAnswerAloneKeepsEveryRefusal(t *testing.T) {
	w, ui := browsing(t)
	code, body := submit(t, ui, url.Values{"target": {"postgres://not-in-the-catalog"}, "q": {"SELECT 1"}}, true)
	if code != http.StatusOK || !strings.Contains(body, strings.SplitN(pages.Unknown, ".", 2)[0]) {
		t.Errorf("an unknown connection is not refused in the pane: %d %q", code, body)
	}
	if len(w.calls) != 0 {
		t.Errorf("a refused body still reached the warehouse: %v", w.calls)
	}

	code, _ = submit(t, ui, url.Values{"target": {probeTarget}, "q": {strings.Repeat("x", 70<<10)}}, true)
	if code != http.StatusBadRequest {
		t.Errorf("a statement past the ceiling answered %d with the header", code)
	}
}

// A GET IGNORES THE HEADER: it runs nothing and draws the workbench, because
// the answer to opening a screen is the screen.
func TestAGetIgnoresTheFragmentHeader(t *testing.T) {
	w, ui := browsing(t)
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, "/sql", nil)
	r.Header.Set("X-Brevis-Fragment", "answer")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Error("a GET with the header did not draw the workbench")
	}
	for _, c := range w.calls {
		if c != "/v1/objects" {
			t.Errorf("a GET asked the warehouse %s", c)
		}
	}
}

// A RUN REPLACES THE ANSWER AND NOTHING ELSE, AND FALLS BACK TO THE FORM.
//
// The workbench posted itself on every Run: the page was thrown away, the
// server drew a bare <textarea>, and CodeMirror replaced it after the first
// paint -- the one flash View Transitions could not hide. Now a Run fetches
// the answer alone and swaps it into the pane, then wires the new pane (its
// pager, its Recent entry), and the editor, the tabs and the tree are never
// touched.
//
// THE FORM IS STILL THE MECHANISM. Anything the fetch cannot handle -- no
// network, a non-2xx, a session that expired and was redirected to the login
// page -- submits the form for real, which is what a Run was before. And the
// tree's own buttons (expand, connect) still submit: they are not Runs.
//
// NOTHING REACHES THE ADDRESS BAR. The body is built with encodeURIComponent,
// as the cost line's is, and the island never touches `history` or
// `location`.
func TestARunReplacesOnlyTheAnswer(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)

	run := islandFunc(js, "runInPlace")
	if run == "" {
		t.Fatal("the island has no runInPlace: a Run still reloads the workbench")
	}
	for _, want := range []string{
		`"X-Brevis-Fragment": "answer"`, "fetch(", "encodeURIComponent",
		".redirected", "form.submit()", "wirePager(pane)", "addRecent(pane,",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("runInPlace never uses %s", want)
		}
	}
	if !strings.Contains(js, "ev.submitter") {
		t.Error("the submit handler cannot tell a Run from the tree's own buttons")
	}
	for _, gone := range []string{"URLSearchParams", "history.", "location."} {
		if strings.Contains(js, gone) {
			t.Errorf("the island uses %s, which is how a statement reaches the address bar", gone)
		}
	}
}

// A RUN THAT TAKES A WHILE SAYS SO, AND CAN BE CANCELLED.
//
// After 120ms the pane shows "Running" and a live timer -- not before, because
// a status that flashes for 40ms is the flicker this whole slice removes. The
// previous answer stays under it, so cancelling leaves the pane exactly as it
// was, never half-drawn.
//
// "CANCEL" IS A MEASURED WORD, NOT A HOPE. Aborting the request cancels the
// request context, and the query with it: on Postgres a `pg_sleep(9)` active
// at 1.5s was gone from pg_stat_activity half a second after the abort
// (2026-10-15); on BigQuery `serve` asks jobs.cancel for a job whose caller
// gave up. A label promising that and delivering a hidden spinner would be
// money spent on a query nobody is waiting for.
//
// The markup is the page's (Tailwind reads templates, not the island); the
// island clones it.
func TestASlowRunSaysSoAndCanBeCancelled(t *testing.T) {
	_, ui := browsing(t)
	_, page := get(t, ui, "/sql")
	at := strings.Index(page, "data-running-template")
	if at < 0 {
		t.Fatal("the workbench has no running state to show")
	}
	tpl := page[at:]
	tpl = tpl[:strings.Index(tpl, "</template>")]
	for _, want := range []string{`role="status"`, "data-running-elapsed", "data-stop", `type="button"`, ">Cancel<"} {
		if !strings.Contains(tpl, want) {
			t.Errorf("the running state lacks %s", want)
		}
	}

	_, raw := get(t, ui, "/assets/sql.js")
	run := islandFunc(strip(raw), "runInPlace")
	for _, want := range []string{"AbortController", ".abort()", `"AbortError"`, "120", "data-running-elapsed", "[data-stop]"} {
		if !strings.Contains(run, want) {
			t.Errorf("runInPlace never uses %s", want)
		}
	}
}
