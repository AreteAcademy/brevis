package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// THE CONNECTION IS A PICKER AGAIN, AT THE TOP OF THE TREE (screen-sql T1).
//
// The roots let somebody compare two warehouses without losing the tree; the
// handoff's picker shows one at a time with its logo, and the owner chose it.
// A form GET, so with no script choosing one is a page; a bucket is still not
// offered, since no SELECT can name one.
func TestTheConnectionIsAPickerAtTheTopOfTheTree(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")
	pick := body[strings.Index(body, "data-connection-pick"):]
	pick = pick[:strings.Index(pick, "</form>")]
	if !strings.Contains(pick, `<option value="`+probeTarget+`" selected`) || !strings.Contains(body, `<form method="get" action="/sql" data-connection-pick`) {
		t.Errorf("the catalog's destination is not the chosen connection: %s", pick)
	}
	if strings.Contains(pick, "file:///data/landing/") {
		t.Error("a bucket is offered as a connection")
	}
	if strings.Contains(body, `name="connect" value="`) {
		t.Error("the connections are still roots of the tree")
	}
}

// A TABLE IN THE LINK IS A SELECT IN THE BOX (screen-sql T1): the Catalog,
// Loads, Streams and Endpoints link here with a destination, and the
// workbench opens on it with `SELECT * … LIMIT 100` written -- and RUNS
// NOTHING: a query is a cost, and opening a page is not asking for one. The
// statement is built from a destination the catalog knows, never read from
// the link.
func TestATableInTheLinkIsASelectInTheBox(t *testing.T) {
	w, ui := browsing(t)
	_, body := get(t, ui, "/sql?target="+url.QueryEscape(probeTarget))
	if !strings.Contains(body, "SELECT *\nFROM bronze.orders\nLIMIT 100;</textarea>") {
		t.Errorf("the box does not hold the table's SELECT: %s", body[strings.Index(body, "<textarea"):strings.Index(body, "</textarea>")+11])
	}
	for _, path := range w.calls {
		if path == "/v1/query" {
			t.Error("opening the link ran the query")
		}
	}
	_, body = get(t, ui, "/sql")
	if strings.Contains(body, "LIMIT 100;</textarea>") {
		t.Error("a workbench opened with no destination wrote a SELECT")
	}
}

// CHOOSING A CONNECTION IS NOT RUNNING A QUERY, for the reason expanding a
// relation is not: the tree's buttons submit the EDITOR's form, which is how
// a half-written statement survives the click.
func TestChoosingAConnectionRunsNothing(t *testing.T) {
	w, ui := browsing(t)
	mux := http.NewServeMux()
	ui.Registrar(mux)

	form := url.Values{"target": {probeTarget}, "q": {"SELECT 1"}, "connect": {probeTarget}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	for _, p := range w.calls {
		if p == "/v1/query" {
			t.Fatalf("choosing a connection spent a query; calls: %v", w.calls)
		}
	}
	if !strings.Contains(rec.Body.String(), "SELECT 1") {
		t.Error("the statement did not survive the click")
	}
}

// THE COLUMNS ARRIVE AS A FRAGMENT, which is the whole point of this slice:
// opening a relation reloaded the entire screen, because the only way to ask
// was to submit the form.
//
// A FRAGMENT AND NOT JSON. The spec said JSON; this is HTML, and the reason
// is that the markup then has exactly one definition. A JSON endpoint means
// the tree's classes, its ARIA and its "this warehouse does not say" exist
// twice -- once in templ, where the Go suite tests them, and once in a
// JavaScript string, where nothing does.
func TestColumnsComeBackAsAFragment(t *testing.T) {
	w, ui := browsing(t)
	code, body := get(t, ui, "/api/sql/columns?target="+url.QueryEscape(probeTarget)+"&schema=sales&name=daily")

	if code != http.StatusOK {
		t.Fatalf("the fragment answered %d: %s", code, body)
	}
	for _, want := range []string{"sku", "text", "quantity", "integer"} {
		if !strings.Contains(body, want) {
			t.Errorf("the fragment does not carry %q:\n%s", want, body)
		}
	}
	// A FRAGMENT IS NOT A PAGE. Returning the whole document would work and
	// would ship the top bar, the fonts and the editor into a <div>.
	if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "<body") {
		t.Error("the fragment is a whole page")
	}
	if len(w.calls) == 0 || w.calls[len(w.calls)-1] != "/v1/columns" {
		t.Errorf("the fragment did not ask the service for columns; calls: %v", w.calls)
	}
}

// AND SO DO A CONNECTION'S OBJECTS, for the connection somebody opened and
// not for all of them: CHECKPOINT D's rule is that a listing costs, so it is
// paid for when it is asked for.
func TestObjectsComeBackAsAFragment(t *testing.T) {
	_, ui := browsing(t)
	code, body := get(t, ui, "/api/sql/objects?target="+url.QueryEscape(probeTarget))

	if code != http.StatusOK {
		t.Fatalf("the fragment answered %d: %s", code, body)
	}
	for _, want := range []string{"sales", "daily", "events", `data-relation-name="sales.daily"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the fragment does not carry %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("the fragment is a whole page")
	}
}

// A SCHEMA AND A TABLE LOOK ALIKE IN TEXT: the same colour, the same size,
// and in a schema with forty tables the eye cannot find where one schema
// ends. An icon in front of each name is what tells them apart -- and it is
// hidden from a screen reader, which already hears the <summary> and the
// button for what they are, and would otherwise announce a picture first.
func TestTheTreeMarksEachSchemaWithAnIcon(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/api/sql/objects?target="+url.QueryEscape(probeTarget))

	icons := iconTags(body, "schema")
	if len(icons) != 1 {
		t.Fatalf("the probe has one schema and the tree draws %d schema icons:\n%s", len(icons), body)
	}
	for _, tag := range icons {
		if !strings.Contains(tag, `aria-hidden="true"`) {
			t.Errorf("a screen reader would announce the icon: %s", tag)
		}
	}
}

// AND EACH TABLE WITH ANOTHER, inside the button that offers its SELECT --
// which must still show exactly the name, icon or not.
func TestTheTreeMarksEachTableWithAnIcon(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/api/sql/objects?target="+url.QueryEscape(probeTarget))

	icons := iconTags(body, "table")
	if len(icons) != 2 {
		t.Fatalf("the probe has two tables and the tree draws %d table icons:\n%s", len(icons), body)
	}
	for _, tag := range icons {
		if !strings.Contains(tag, `aria-hidden="true"`) {
			t.Errorf("a screen reader would announce the icon: %s", tag)
		}
	}
	at := strings.Index(body, `data-relation-name="sales.daily"`)
	if at < 0 {
		t.Fatalf("no button offers sales.daily:\n%s", body)
	}
	button, _, _ := strings.Cut(body[at:], "</button>")
	if len(iconTags(button, "table")) != 1 {
		t.Errorf("the icon is not inside the button that offers the table:\n%s", button)
	}
	if !strings.Contains(button, ">daily</span>") {
		t.Errorf("the button no longer shows the table's name on its own:\n%s", button)
	}
}

// iconTags is every opening <svg> tag carrying `data-icon="kind"`.
func iconTags(body, kind string) []string {
	var out []string
	for _, chunk := range strings.Split(body, "<svg")[1:] {
		tag, _, _ := strings.Cut(chunk, ">")
		if strings.Contains(tag, `data-icon="`+kind+`"`) {
			out = append(out, tag)
		}
	}
	return out
}

// THE FRAGMENTS OBEY THE CATALOG TOO. They are a second door into the same
// service, and a rule enforced at one door is not a rule.
func TestTheFragmentsRefuseATargetTheCatalogDoesNotKnow(t *testing.T) {
	for _, path := range []string{
		"/api/sql/objects?target=bigquery://elsewhere/x/y",
		"/api/sql/columns?target=bigquery://elsewhere/x/y&schema=a&name=b",
	} {
		t.Run(path, func(t *testing.T) {
			w, ui := browsing(t)
			code, _ := get(t, ui, path)
			if code != http.StatusNotFound {
				t.Errorf("answered %d, wanted 404", code)
			}
			if len(w.calls) != 0 {
				t.Errorf("it reached the service anyway: %v", w.calls)
			}
		})
	}
}

// ONE DEFINITION, TWO DOORS. The page draws a relation's columns and so does
// the fragment, and if they are two pieces of markup they will drift -- which
// is the same failure as the catalog rule that existed in one path and not
// the other, three commits ago.
func TestThePageAndTheFragmentDrawTheSameColumns(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/api/sql/columns?target="+
		url.QueryEscape(probeTarget)+"&schema=sales&name=daily")
	frag := strings.TrimSpace(raw)
	page := expand(t, ui, probeTarget, "", "", "sales.daily")

	if frag == "" {
		t.Fatal("the fragment is empty")
	}
	if !strings.Contains(page, frag) {
		t.Errorf("the page does not draw what the fragment draws:\n--- fragment ---\n%s", frag)
	}
}

// THE TREE CAN BE FILTERED: one connection's listing is on the page, and the
// box over it narrows it as somebody types.
func TestTheTreeHasAFilter(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")
	if !strings.Contains(body, `type="search"`) || !strings.Contains(body, `placeholder="Filter tables"`) {
		t.Error("there is no filter over the tree")
	}
}

// THE ISLAND READS THE BUTTONS THE TREE WRITES. Four tests once agreed the
// page referenced `/assets/sql.js` while it 404'd; this is the same class of
// agreement, between two files that have to name the same thing.
func TestTheIslandInterceptsTheButtonsTheTreeWrites(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/sql.js")

	// STRIPPED OF ITS COMMENTS, because this test could not fail. Four of
	// the five strings below are also in the paragraphs that explain the
	// code, so an island that stopped intercepting anything at all still
	// passed -- it was reading the prose about the code instead of the code.
	js := strip(raw)

	for _, want := range []string{
		`"expand"`, "data-select", "/api/sql/columns", "[data-connection-pick] select", "preventDefault",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the island's CODE never uses %q, so that click still reloads the page", want)
		}
	}
}
