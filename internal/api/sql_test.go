package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// runSQL submits the workbench's form, the way a browser does.
func runSQL(t *testing.T, ui *api.UI, target, statement string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"target": {target}, "q": {statement}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("/sql answered %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// A WORKBENCH IS NOT OPENED FROM A DESTINATION, which is the whole reason it
// is a screen of its own. `/data` answers "what landed and is it on time";
// this answers "what is in it", and the second question is not asked one
// table at a time.
func TestTheWorkbenchRunsAQueryAndDrawsIt(t *testing.T) {
	f := &sqlFake{body: `{"columns":["sku","total"],"rows":[["W-102",10],["W-100",6]],
		"truncated":false,"limit":100,"bytes":5242880,"ms":31}`}
	svc := f.start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := runSQL(t, ui, probeTarget, "SELECT sku, sum(quantity) AS total FROM t GROUP BY sku")

	if got, _ := f.asked["statement"].(string); !strings.Contains(got, "sum(quantity)") {
		t.Errorf("the service was sent %q", got)
	}
	for _, want := range []string{">sku<", ">total<", "W-102", "5.0 MB", "31 ms"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
}

// THE STATEMENT NEVER REACHES A URL, which is V2's rule and the reason this
// screen has no tabs yet: tab state in a query parameter would be SQL in a
// query parameter.
func TestTheWorkbenchPostsAndKeepsSQLOutOfTheLink(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	body := render(t, consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery"), "/sql")

	if !strings.Contains(body, `method="post"`) {
		t.Error("the workbench's form is not a POST")
	}
	if strings.Contains(body, "?q=") || strings.Contains(body, "&q=") {
		t.Error("a statement is carried in a link")
	}
}

// IT LISTS THE DESTINATIONS THE CATALOG KNOWS, and only the ones a SELECT can
// name. A bucket has no columns; offering it in a connection picker is the
// same mistake as a tab on one.
func TestThePickerOffersRelationsAndNotBuckets(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/sql")
	if !strings.Contains(body, probeTarget) {
		t.Errorf("the picker does not offer %q", probeTarget)
	}
	for _, gone := range []string{"file:///data/landing/", "pubsub://"} {
		if strings.Contains(body, gone) {
			t.Errorf("the picker offers %q, which no SELECT can name", gone)
		}
	}
}

// A REFUSAL IS A SENTENCE WHERE THE GRID WOULD BE, and the statement comes
// back into the box. Same rules as the destination page, because it is the
// same service saying them.
func TestTheWorkbenchKeepsTheScreenOnARefusal(t *testing.T) {
	f := &sqlFake{status: http.StatusBadRequest,
		body: `{"error":"this service only reads, and this is a DROP statement"}`}
	svc := f.start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := runSQL(t, ui, probeTarget, "DROP TABLE bronze.orders")

	if !strings.Contains(body, "this is a DROP statement") {
		t.Error("the reason is not on the page")
	}
	if !strings.Contains(body, "DROP TABLE bronze.orders") {
		t.Error("the statement was not put back in the box")
	}
}

// NO SERVICE, NO SCREEN. A workbench that can only ever say "not configured"
// is a section in the bar that leads nowhere -- the same rule the tabs follow.
func TestWithoutAServiceTheWorkbenchIsNotOffered(t *testing.T) {
	ui := consoleOf(t, nil, signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/data")
	if strings.Contains(body, `href="/sql"`) {
		t.Error("the bar offers /sql with no SQL service configured")
	}

	// AND NOT BY HIDING A LINK. Somebody typing the path gets the same
	// answer -- a section that does not exist, rather than a box that can
	// only ever say "not configured".
	mux := http.NewServeMux()
	ui.Registrar(mux)
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/sql", nil),
		httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader("q=SELECT+1")),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s /sql answered %d with no service configured", r.Method, rec.Code)
		}
	}
}

// THE DESTINATION PAGE SHEDS THE QUERY TAB, which is the separation the
// workbench exists for.
//
// Preview STAYS, because it belongs to a destination: it is "show me this
// table", asked about the thing already on the screen. A query is not about
// one table -- it joins, it groups, it reaches across -- and a tab that
// pretends otherwise is what made both screens worse.
func TestADestinationPreviewsButDoesNotQuery(t *testing.T) {
	svc := (&sqlFake{body: `{"columns":["k"],"rows":[["1"]]}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/data/target?u="+probeTarget)
	if !strings.Contains(body, "tab=preview") {
		t.Error("the destination lost its Preview")
	}
	if strings.Contains(body, "tab=query") {
		t.Error("the destination still offers a Query tab")
	}
}

// AND IT POINTS AT THE WORKBENCH, carrying the destination. Somebody who
// wanted more than a preview should not have to go and find the connection
// again in a picker.
func TestADestinationLinksToTheWorkbenchCarryingItself(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/data/target?u="+probeTarget)
	want := "/sql?target=" + url.QueryEscape(probeTarget)
	if !strings.Contains(body, want) {
		t.Errorf("the destination does not link to %q", want)
	}
}

// THE WORKBENCH OPENS ON THE DESTINATION IT WAS SENT. A TARGET in a URL is
// fine -- this console already puts one in `/data/target?u=` -- and a
// STATEMENT is not, which is the distinction the POST keeps.
func TestTheWorkbenchOpensOnTheTargetItWasGiven(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/sql?target="+url.QueryEscape(probeTarget))
	if !strings.Contains(body, `value="`+probeTarget+`" selected`) {
		t.Errorf("the picker did not open on %q", probeTarget)
	}
}

// A TARGET NOBODY LANDED ON IS NOT OPENED. The picker offers what the catalog
// knows; a parameter naming anything else is ignored rather than trusted,
// which is the rule `/data/target` already follows by answering 404.
func TestTheWorkbenchIgnoresATargetTheCatalogDoesNotKnow(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/sql?target="+url.QueryEscape("postgres://somewhere/else/entirely"))
	if strings.Contains(body, "somewhere/else/entirely") {
		t.Error("the workbench opened on a destination nothing landed on")
	}
	if !strings.Contains(body, `value="`+probeTarget+`" selected`) {
		t.Error("it did not fall back to the first destination it knows")
	}
}

// AN EMPTY BOX ASKS NOTHING, and opening the screen asks nothing either.
//
// Both moved here from the destination page's Query tab when that tab went
// away: the property is about a workbench and a warehouse query for a page
// view is a bill for a mistake, wherever the box lives.
func TestTheWorkbenchAsksNothingUntilItIsAsked(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer svc.Close()
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	render(t, ui, "/sql")
	if asked {
		t.Error("opening the workbench ran a query")
	}
	runSQL(t, ui, probeTarget, "   \n\t ")
	if asked {
		t.Error("an empty box reached the warehouse")
	}
}

// A STATEMENT IN THE URL IS NOT A STATEMENT, moved here with the form.
//
// This is the property the POST exists for, and it holds against a link
// somebody crafted rather than against the form the page draws: `?q=…` is
// ignored, so a query cannot be put in a browser's history, a proxy log or a
// Referer header by anybody -- including by a page on another site linking
// here. `PostFormValue` is what does it, by reading the body and never the
// query string.
func TestAStatementInTheWorkbenchURLIsIgnored(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer svc.Close()
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	render(t, ui, "/sql?target="+url.QueryEscape(probeTarget)+"&q="+url.QueryEscape("SELECT 1"))
	if asked {
		t.Error("a statement in the URL was run")
	}

	// AND NOT EVEN WHEN THE REQUEST IS A POST.
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/sql?q="+url.QueryEscape("SELECT 1")+"&target="+url.QueryEscape(probeTarget),
		strings.NewReader("target="+url.QueryEscape(probeTarget)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, r)
	if asked {
		t.Error("a POST took its statement from the URL")
	}
}

// THE EDITOR IS AN ISLAND, AND ONLY THIS SCREEN PAYS FOR IT.
//
// `base.templ` states the rule -- "interactive islands only where the
// interaction justifies one" -- and the DAG's 350 KB of React already follows
// it. A SQL editor is the same rule applied, not a new one.
func TestTheEditorLoadsOnlyOnTheWorkbench(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	workbench := render(t, ui, "/sql")
	for _, want := range []string{
		"/assets/vendor/codemirror.js",
		"/assets/vendor/codemirror-sql.js",
		"/assets/vendor/codemirror.css",
		"/assets/sql.js",
	} {
		if !strings.Contains(workbench, want) {
			t.Errorf("the workbench does not load %q", want)
		}
	}

	for _, path := range []string{"/data", "/data/target?u=" + probeTarget} {
		body := render(t, ui, path)
		if strings.Contains(body, "codemirror") {
			t.Errorf("%s pays for the editor and never uses it", path)
		}
	}
}

// AND THE PAGE WORKS WITHOUT IT. The island MOUNTS ON the textarea rather
// than replacing it: a console where the form only submits with JavaScript is
// not this console, and it is also how the whole suite tests the workbench
// without a browser.
func TestTheWorkbenchIsAFormBeforeItIsAnEditor(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	body := render(t, consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery"), "/sql")

	textarea := strings.Index(body, `<textarea`)
	if textarea < 0 {
		t.Fatal("there is no textarea; the form cannot work without the island")
	}
	if !strings.Contains(body, `name="q"`) {
		t.Error("the textarea does not carry the field name the handler reads")
	}
	// The island's script comes AFTER the form, so the element it mounts on
	// exists by the time it runs.
	if js := strings.Index(body, "/assets/sql.js"); js < textarea {
		t.Error("the editor's script runs before the textarea it mounts on")
	}
}

// EVERY ASSET A PAGE ASKS FOR IS ACTUALLY SERVED.
//
// `/assets/sql.js` 404'd on the running console while four tests agreed the
// page referenced it: the embed directive is an explicit list and a new file
// is not in it. Referencing a path and serving one are different facts, and
// every test here had been checking the first.
//
// It walks the HTML rather than naming the files, so the next asset somebody
// adds is covered the moment the page asks for it.
func TestEveryAssetTheWorkbenchAsksForIsServed(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	mux := http.NewServeMux()
	ui.Registrar(mux)
	body := render(t, ui, "/sql")

	refs := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(refs) < 4 {
		t.Fatalf("the page asks for %d assets, which is fewer than this screen needs", len(refs))
	}
	for _, m := range refs {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, m[1], nil))
		if rec.Code != http.StatusOK {
			t.Errorf("the page asks for %s and it answers %d", m[1], rec.Code)
		}
	}
}
