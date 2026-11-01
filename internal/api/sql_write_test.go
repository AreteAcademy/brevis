package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A WRITE IS NOT AN EMPTY GRID.
//
// `brevis-sql serve` will run a statement that changes data on a connection
// somebody unlocked, and the reply carries no columns and no rows. Drawn
// through the grid that answers a SELECT, that is a table with no headings
// and nothing under them -- which reads as "it found nothing" for a statement
// that found nothing because it was never looking.
//
// The only question anybody asks after a write is how much changed, so that
// is what the panel says.
func TestAWriteSaysWhatItChangedInsteadOfDrawingAnEmptyGrid(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":[],"rows":[],"wrote":true,"changed":12,"bytes":0,"ms":7}`
	body := ran(t, ui, "UPDATE t SET n = 1")

	if !strings.Contains(body, "12 rows changed") {
		t.Errorf("the screen never says what changed")
	}
	// AND THE GRID IS NOT THERE AT ALL. A `<table>` with no `<th>` is a
	// shape a reader has to interpret; an absent one is not.
	at := strings.Index(body, `data-panel="results"`)
	if at < 0 {
		t.Fatal("there is no results panel")
	}
	panel := body[at:]
	if end := strings.Index(panel, `data-panel="json"`); end > 0 {
		panel = panel[:end]
	}
	if strings.Contains(panel, "<table") {
		t.Errorf("a write drew a grid:\n%s", panel)
	}
}

// ONE ROW IS ONE ROW, which is the plural every count gets wrong once.
func TestOneChangedRowIsSingular(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":[],"rows":[],"wrote":true,"changed":1}`
	body := ran(t, ui, "DELETE FROM t WHERE n = 1")
	if !strings.Contains(body, "1 row changed") || strings.Contains(body, "1 rows changed") {
		t.Error("the sentence does not read for one row")
	}
}

// ZERO IS AN ANSWER AND NOT AN ABSENCE.
//
// An UPDATE whose WHERE matched nothing is the single most useful thing this
// panel can report, because it is the one a person gets wrong silently.
func TestAWriteThatMatchedNothingSaysZero(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":[],"rows":[],"wrote":true,"changed":0}`
	body := ran(t, ui, "UPDATE t SET n = 1 WHERE FALSE")
	if !strings.Contains(body, "0 rows changed") {
		t.Error("an update that matched nothing did not say so")
	}
}

// AND A STATEMENT THAT COUNTS NO ROWS SAYS SO, rather than claiming zero.
//
// `CREATE TABLE` changed nothing and did not fail. "0 rows changed" would
// read as a failure to whoever ran it, and they would run it again.
func TestADDLSaysItChangesNoRowsRatherThanZero(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":[],"rows":[],"wrote":true}`
	body := ran(t, ui, "CREATE TABLE t (n int)")

	if strings.Contains(body, "0 rows changed") {
		t.Error("a DDL claimed it changed zero rows")
	}
	if !strings.Contains(body, "no rows to count") {
		t.Error("the screen does not say why there is no count")
	}
}

// AND A SELECT IS UNTOUCHED. Everything above is behind one flag, and a
// console that started describing queries as writes would be worse than the
// empty grid this replaces.
func TestAQueryStillDrawsItsGrid(t *testing.T) {
	w, ui := browsing(t)
	w.query = `{"columns":["n"],"rows":[["1"],["2"]]}`
	body := ran(t, ui, "SELECT n FROM t")

	if strings.Contains(body, "changed") {
		t.Error("a query was described as a write")
	}
	if !strings.Contains(body, "<table") {
		t.Error("a query drew no grid")
	}
}

// A REFUSAL AT RUN IS TITLED BY THE CONSOLE (screen-sql T3): the service
// says no without saying why, so the console says "refused" once, shows the
// service's sentence as it came, and where writes run -- and History records
// it as a failure.
func TestARefusalAtRunIsTitledByTheConsole(t *testing.T) {
	w, ui := browsing(t)
	w.status["/v1/query"] = http.StatusBadRequest
	w.query = `{"error":"this service only reads, and this is a DELETE statement"}`
	_, body := submit(t, ui, url.Values{"target": {probeTarget}, "q": {"DELETE FROM sales.daily"}}, true)
	for _, want := range []string{"Refused by the SQL service", "this service only reads, and this is a DELETE statement",
		`href="/workflows"`, `data-outcome="failed"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal lacks %s: %s", want, body)
		}
	}
}
