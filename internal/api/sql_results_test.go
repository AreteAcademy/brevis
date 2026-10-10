package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ran posts a statement and returns the screen.
func ran(t *testing.T, ui interface{ Registrar(*http.ServeMux) }, statement string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"target": {probeTarget}, "q": {statement}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("/sql answered %d", rec.Code)
	}
	return rec.Body.String()
}

// TWO TABS, AND THE REFERENCE'S OTHER FOUR ARE NOT DRAWN.
//
// BigQuery's panel offers Job information, Visualization, Execution details
// and Execution graph. Those are its JOB MODEL, which `serve` does not have:
// drawing the tabs would be four promises the console cannot keep, and an
// empty tab is worse than an absent one because somebody clicks it.
func TestTheResultPanelHasTwoTabsAndNotSix(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"]]}`
	body := ran(t, ui, "SELECT 1")

	for _, want := range []string{`data-tab="results"`, `data-tab="json"`, `role="tablist"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the panel has no %s", want)
		}
	}
	for _, gone := range []string{"Execution graph", "Job information", "Visualization"} {
		if strings.Contains(body, gone) {
			t.Errorf("the panel offers %q, which this service cannot answer", gone)
		}
	}
}

// EVERY ROW IS NUMBERED, AND THE NUMBER DOES NOT COME WITH THE DATA.
//
// A row number is navigation, not a value: somebody who selects the grid and
// pastes it into a sheet must get the warehouse's columns and not an extra
// one this console invented.
func TestEveryRowIsNumberedAndTheNumberIsNotData(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["a"],["b"],["c"]]}`
	body := ran(t, ui, "SELECT n")

	if n := strings.Count(body, `data-rownum`); n != 3 {
		t.Errorf("%d rows are numbered, the result has 3", n)
	}
	_, css := get(t, ui, "/assets/app.css")
	if !strings.Contains(css, "user-select") {
		t.Error("the stylesheet that is served never makes the row number unselectable")
	}
}

// NULL IS A WORD, AND AN EMPTY STRING IS NOT IT.
//
// One is "nothing was recorded" and the other is a value somebody wrote.
// They were an em dash and a blank, which distinguishes them but makes a
// reader guess which is which; `null` is the word every warehouse console
// uses and the one the warehouse itself uses.
func TestNullIsDrawnAsTheWordAndAnEmptyStringIsNot(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["a","b"],"rows":[[null,""]]}`
	body := ran(t, ui, "SELECT a, b")

	at := strings.Index(body, `data-panel="results"`)
	if at < 0 {
		t.Fatal("there is no results panel")
	}
	grid := body[at:]
	if !strings.Contains(grid, ">null<") {
		t.Error("a NULL is not drawn as null")
	}
	// And the empty string is not drawn as the word.
	if strings.Count(grid, ">null<") != 1 {
		t.Error("an empty string was drawn as null too")
	}
}

// A NUMBER LINES UP, AND THE RULE READS THE VALUE AND NOT ITS GO TYPE.
//
// Measured against both warehouses: BigQuery's REST API returns EVERY scalar
// as a JSON string -- `SELECT 1, 1.5, true` comes back `["1","1.5","true"]`
// -- while Postgres hands over typed values. A rule that looked at the Go
// type would line up one warehouse's integers and not the other's, for the
// same query. So it reads what the cell will say.
func TestANumberLinesUpAndAWordDoesNot(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n","s"],"rows":[["1234","hello"]]}`
	body := ran(t, ui, "SELECT n, s")

	at := strings.Index(body, ">1234<")
	if at < 0 {
		t.Fatal("the number is not on the screen")
	}
	// The cell that holds it, read backwards to its own tag.
	open := strings.LastIndex(body[:at], "<td")
	if open < 0 {
		t.Fatal("the number is not in a cell")
	}
	if !strings.Contains(body[open:at], "text-right") {
		t.Errorf("a number does not line up: %s", body[open:at])
	}
	word := strings.Index(body, ">hello<")
	wopen := strings.LastIndex(body[:word], "<td")
	if strings.Contains(body[wopen:word], "text-right") {
		t.Error("a word lines up as though it were a number")
	}
}

// THE PAGER COUNTS WHAT CAME BACK, AND NEVER PROMISES A TOTAL IT WAS NOT
// GIVEN.
//
// A limit cut the answer, so the number of rows in hand is a FLOOR and not a
// count. "1-50 of 500" is a sentence somebody reads a MAX off and is wrong
// about; "500+" is the same fact without the lie.
func TestThePagerNeverPrintsATotalItWasNotGiven(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"],["2"],["3"]],"truncated":true}`
	body := ran(t, ui, "SELECT n")

	if !strings.Contains(body, `data-total="3+"`) {
		t.Error("a truncated result claims an exact total")
	}

	w.query = `{"columns":["n"],"rows":[["1"],["2"],["3"]]}`
	body = ran(t, ui, "SELECT n")
	if !strings.Contains(body, `data-total="3"`) {
		t.Error("a whole result does not say how many rows it holds")
	}
}

// THE PAGER IS AN ENHANCEMENT, AND WITHOUT THE ISLAND THE WHOLE ANSWER IS
// ON THE PAGE.
//
// It pages over rows that have already arrived -- no second query, no link,
// no form. So the server draws every row and hides the controls; a browser
// with no script shows all of them, which is exactly what this screen did
// before, rather than a row of buttons that do nothing.
func TestThePagerCostsNoQueryAndHidesItselfWithoutTheIsland(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"],["2"],["3"]]}`
	before := len(w.calls)
	body := ran(t, ui, "SELECT n")

	if len(w.calls)-before > 2 {
		t.Errorf("drawing the pager cost extra calls: %v", w.calls[before:])
	}
	at := strings.Index(body, "data-pager")
	if at < 0 {
		t.Fatal("there is no pager")
	}
	el := body[at:]
	if end := strings.Index(el, ">"); end > 0 {
		el = el[:end]
	}
	if !strings.Contains(el, "hidden") {
		t.Errorf("the pager is drawn before anything can drive it: %s", el)
	}
	// Every row is on the page, not just the first page of them.
	if n := strings.Count(body, "data-rownum"); n != 3 {
		t.Errorf("the page holds %d rows of 3; without a script the rest are unreachable", n)
	}
}

// AND THE ISLAND DRIVES WHAT THE PANEL DRAWS.
//
// Comments stripped, because that is what the last slice learned: four of
// five assertions against this file were once satisfied by the paragraphs
// explaining the code rather than the code.
func TestTheIslandDrivesTheTabsAndThePager(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)

	for _, want := range []string{
		"data-tab", "data-panel", "data-pager-next", "data-pager-prev",
		"data-total", "data-per-page-pick",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the island's CODE never uses %q, so that control is inert", want)
		}
	}
	// AND IT NEVER ASKS AGAIN. A pager that fetched would be a warehouse
	// billed because somebody wanted rows 51 to 100.
	//
	// SCOPED TO THE FUNCTION, which it was not: this read from
	// `function wireResults` to the END OF THE FILE, so it was asserting
	// that nothing BELOW the pager ever fetches. It passed for as long as
	// the pager happened to be last, and failed the day an unrelated
	// function was added after it.
	wired := islandFunc(js, "wireResults")
	if wired == "" {
		t.Fatal("the results are not wired at all")
	}
	if strings.Contains(wired, "fetch(") {
		t.Error("paging the answer asks the service again")
	}
}

// islandFunc is one top-level function of the island, by name.
//
// The island is one IIFE whose own functions are indented two spaces and
// whose nested ones are indented more, so the next `\n  function ` is where
// this one ends. A crude boundary, and a real one: without it an assertion
// about a function is an assertion about the rest of the file.
func islandFunc(js, name string) string {
	at := strings.Index(js, "function "+name)
	if at < 0 {
		return ""
	}
	rest := js[at:]
	if end := strings.Index(rest[1:], "\n  function "); end >= 0 {
		return rest[:end+1]
	}
	return rest
}
