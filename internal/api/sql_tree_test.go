package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// EVERY CONNECTION IS A ROOT, AND THE PICKER IS GONE.
//
// A <select> says which warehouse and nothing about what is in it, so
// comparing two of them meant choosing one, losing the tree, and choosing the
// other. One tree with the connection as its root node is the shape the
// reference uses and the one a person actually reads.
func TestEveryConnectionIsARootInTheTree(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	if strings.Contains(body, `<select id="target"`) {
		t.Error("the connection picker is still on the screen")
	}
	if !strings.Contains(body, `name="connect" value="`+probeTarget+`"`) {
		t.Errorf("the catalog's destination is not a node in the tree")
	}
	// A bucket is still not offered: no SELECT can name one.
	if strings.Contains(body, `name="connect" value="file:///data/landing/"`) {
		t.Error("a bucket is offered as a connection")
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
	for _, want := range []string{"sales", "daily", "events", `data-insert="sales.daily"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the fragment does not carry %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("the fragment is a whole page")
	}
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

// A CONNECTION NOBODY OPENED HAS NOT BEEN SEARCHED, and the tree says so.
//
// The search box filters what is loaded. A warehouse whose listing was never
// fetched holds names the filter cannot see, and a search that quietly skips
// one is how somebody concludes a table does not exist.
func TestTheTreeSaysWhichConnectionsAreNotLoadedYet(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	if !strings.Contains(body, `data-loaded="true"`) {
		t.Error("the open connection is not marked as loaded")
	}
	if !strings.Contains(body, `type="search"`) {
		t.Error("there is no search box over the tree")
	}
}

// THE ISLAND READS THE BUTTONS THE TREE WRITES. Four tests once agreed the
// page referenced `/assets/sql.js` while it 404'd; this is the same class of
// agreement, between two files that have to name the same thing.
func TestTheIslandInterceptsTheButtonsTheTreeWrites(t *testing.T) {
	_, ui := browsing(t)
	_, js := get(t, ui, "/assets/sql.js")

	for _, want := range []string{"expand", "connect", "/api/sql/columns", "/api/sql/objects"} {
		if !strings.Contains(js, want) {
			t.Errorf("the island never mentions %q, so that click still reloads the page", want)
		}
	}
}
