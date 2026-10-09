package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
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

// AN EMPTY BOX RUNS NOTHING, and opening the screen runs nothing either.
//
// Both moved here from the destination page's Query tab when that tab went
// away: the property is about a workbench and a warehouse query for a page
// view is a bill for a mistake, wherever the box lives.
//
// IT IS NO LONGER "ASKS NOTHING". The tree costs one metadata call per
// render, which the service answers from its own memory after the first --
// so the line this draws is between a SCAN, which is what a statement buys
// and what a page view must never buy, and a listing, which is bounded and
// cached. Both paths are named here so that moving a page view onto the
// expensive one has to come through this test.
func TestTheWorkbenchAsksNothingUntilItIsAsked(t *testing.T) {
	asked, paths := false, []string{}
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Query != "" {
			asked = true
		}
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer svc.Close()
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	render(t, ui, "/sql")
	if asked {
		t.Error("opening the workbench ran a query")
	}
	if want := []string{"/v1/objects"}; !slices.Equal(paths, want) {
		t.Errorf("opening the workbench called %v, not %v", paths, want)
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
	// RAN, not CONTACTED. The page asks this same service what the
	// connection holds on every render, so "the service was called" stopped
	// being the question the moment the tree arrived -- the question is
	// whether a STATEMENT crossed, and that is a field in the body.
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Query != "" {
			asked = true
		}
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

// THE TREE, GROUPED BY SCHEMA, for the connection the picker is on.
//
// It is the half of the references this console did not have: a picker says
// which warehouse, and a tree says what is IN it -- including the relations
// nothing landed on, which is the whole reason somebody opens a workbench
// rather than a destination page.
func TestTheWorkbenchDrawsWhatTheConnectionHolds(t *testing.T) {
	f := &sqlFake{body: `{"relations":[
		{"schema":"bronze","name":"orders"},
		{"schema":"bronze","name":"clicks"},
		{"schema":"gold","name":"daily"}],"truncated":false}`}
	svc := f.start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/sql")
	for _, want := range []string{"bronze", "orders", "clicks", "gold", "daily"} {
		if !strings.Contains(body, want) {
			t.Errorf("the tree does not show %q", want)
		}
	}
	// GROUPED: a schema appears once, not once per table.
	if n := strings.Count(body, ">bronze<"); n != 1 {
		t.Errorf("the schema bronze is drawn %d times", n)
	}
	if got, _ := f.asked["target"].(string); got != probeTarget {
		t.Errorf("the listing was asked for %q", got)
	}
}

// A WAREHOUSE THAT CANNOT SAY IS NOT AN ERROR PAGE. The tree is absent and
// the box still runs queries -- the capability is optional and a console
// that broke without it would be a console that needs it.
func TestAWorkbenchWithoutATreeStillQueries(t *testing.T) {
	f := &sqlFake{status: http.StatusNotImplemented,
		body: `{"error":"this warehouse cannot say what it holds"}`}
	svc := f.start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	body := render(t, ui, "/sql")
	if !strings.Contains(body, `name="q"`) {
		t.Error("the box is gone")
	}
	if !strings.Contains(body, "Run") {
		t.Error("there is no way to run anything")
	}
}

// A CUT LISTING SAYS SO. A browser silently showing half a warehouse is
// worse than one that says it is showing part: somebody would conclude a
// table does not exist.
func TestATruncatedListingIsSaidOnScreen(t *testing.T) {
	f := &sqlFake{body: `{"relations":[{"schema":"bronze","name":"orders"}],"truncated":true}`}
	svc := f.start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	if body := render(t, ui, "/sql"); !strings.Contains(body, "there are more") {
		t.Error("a cut listing is drawn as if it were the whole warehouse")
	}
}

// THE TREE AND THE SCRIPT AGREE ON THE ATTRIBUTE.
//
// The page marks each table with `data-insert` and the island reads it. They
// are two files and nothing but a string joins them: rename one and the tree
// becomes a column of dead buttons that still LOOK right, which no markup
// assertion here would notice.
//
// Same lesson as the asset walk above, one level down: a name referenced and
// a name honoured are different facts.
func TestTheTreeAndTheIslandAgreeOnTheAttribute(t *testing.T) {
	f := &sqlFake{body: `{"relations":[{"schema":"bronze","name":"orders"}]}`}
	svc := f.start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")

	if body := render(t, ui, "/sql"); !strings.Contains(body, `data-insert="bronze.orders"`) {
		t.Error("the tree marks nothing for the editor to insert")
	}

	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/sql.js", nil))
	if !strings.Contains(rec.Body.String(), "data-insert") {
		t.Error("the island never reads data-insert, so the tree inserts nothing")
	}
}

// warehouse is a `serve` that answers each endpoint differently and
// remembers what each was asked. The single-body fake above cannot tell a
// listing from a query, which stopped being enough the moment one page view
// called two endpoints.
type warehouse struct {
	objects string
	columns string
	query   string

	status map[string]int
	asked  map[string]map[string]any
	calls  []string
}

func (w *warehouse) start(t *testing.T) *httptest.Server {
	t.Helper()
	w.asked = map[string]map[string]any{}
	s := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.asked[r.URL.Path] = body
		w.calls = append(w.calls, r.URL.Path)
		if code := w.status[r.URL.Path]; code != 0 {
			rw.WriteHeader(code)
		}
		switch r.URL.Path {
		case "/v1/objects":
			_, _ = rw.Write([]byte(w.objects))
		case "/v1/columns":
			_, _ = rw.Write([]byte(w.columns))
		default:
			_, _ = rw.Write([]byte(w.query))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// expand submits the tree's own button: the form the editor is in, carrying
// the statement in its body.
func expand(t *testing.T, ui *api.UI, target, statement, open, want string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"target": {target}, "q": {statement}, "open": {open}, "expand": {want}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("/sql answered %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func browsing(t *testing.T) (*warehouse, *api.UI) {
	t.Helper()
	w := &warehouse{
		objects: `{"relations":[{"schema":"sales","name":"daily"},{"schema":"sales","name":"events"}]}`,
		columns: `{"columns":[{"name":"sku","type":"text"},{"name":"quantity","type":"integer"}]}`,
		query:   `{"columns":["n"],"rows":[["1"]]}`,
		status:  map[string]int{},
	}
	svc := w.start(t)
	return w, consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, probeTarget, "bigquery")
}

// THE OTHER HALF OF THE TREE: what a relation holds.
//
// CHECKPOINT D made this lazy -- one relation at a time, because a
// project-wide COLUMNS query is the one metadata answer that is genuinely
// large. So the screen asks only for what somebody opened.
func TestExpandingARelationDrawsItsColumns(t *testing.T) {
	w, ui := browsing(t)
	body := expand(t, ui, probeTarget, "", "", "sales.daily")

	for _, want := range []string{"sku", "quantity", "text", "integer"} {
		if !strings.Contains(body, want) {
			t.Errorf("the expanded relation does not show %q", want)
		}
	}
	asked := w.asked["/v1/columns"]
	if asked["schema"] != "sales" || asked["name"] != "daily" {
		t.Errorf("the columns were asked for %+v", asked)
	}
	if asked["target"] != probeTarget {
		t.Errorf("the columns were asked on %q", asked["target"])
	}
}

// EXPANDING IS NOT RUNNING, and the statement survives it.
//
// This is why the tree's buttons submit the FORM rather than following a
// link: a GET would discard the textarea, so somebody with a half-written
// query who clicked a table to remember a column name would lose it. The
// statement travels in the body, which is also the only place a statement is
// allowed to travel.
func TestExpandingKeepsTheStatementAndRunsNothing(t *testing.T) {
	w, ui := browsing(t)
	const half = "SELECT sku, sum(quantity) FROM sales.daily WHERE"
	body := expand(t, ui, probeTarget, half, "", "sales.daily")

	if slices.Contains(w.calls, "/v1/query") {
		t.Error("opening a relation ran the query in the box")
	}
	if !strings.Contains(body, "sum(quantity)") {
		t.Error("the half-written statement was lost")
	}
}

// ONLY THE ONE THAT IS OPEN. A tree that drew every relation's columns
// would be the project-wide COLUMNS query CHECKPOINT D refused, assembled
// one request at a time.
func TestOnlyTheOpenRelationShowsItsColumns(t *testing.T) {
	_, ui := browsing(t)
	body := expand(t, ui, probeTarget, "", "", "sales.daily")

	daily, events := strings.Index(body, ">daily<"), strings.Index(body, ">events<")
	if daily < 0 || events < 0 {
		t.Fatalf("the tree no longer draws both relations (%d, %d)", daily, events)
	}
	// COUNT FIRST, THEN POSITION. The position assertion alone could not
	// fail: `v.Cols` holds one relation's columns, so drawing them under
	// EVERY relation still puts the first `sku` under `daily`, and
	// `strings.Index` finds that one. Measured -- the mutation that removed
	// the condition passed. Counting is what sees it.
	if n := strings.Count(body, ">sku<"); n != 1 {
		t.Errorf("sku is drawn %d times, so columns are under more than the open relation", n)
	}
	sku := strings.Index(body, ">sku<")
	if sku < daily || sku > events {
		t.Errorf("sku is drawn at %d, outside daily (%d) and events (%d)", sku, daily, events)
	}
}

// THE PAGE SENDS WHAT THE HANDLER READS.
//
// `TestRunningAQueryKeepsTheTreeOpen` builds the form by hand, so it proves
// the HANDLER honours `open` and says nothing about whether the PAGE ever
// sends it -- measured: deleting the hidden field left that test green. Same
// shape as the asset walk, one field down: a name read and a name sent are
// different facts.
func TestTheFormCarriesWhatIsOpenAndTheTreeSubmitsIt(t *testing.T) {
	_, ui := browsing(t)
	body := expand(t, ui, probeTarget, "", "", "sales.daily")

	if !strings.Contains(body, `name="open" value="sales.daily"`) {
		t.Error("the form does not carry the relation that is open")
	}
	// AND THE TREE'S BUTTONS REACH THAT FORM. They sit outside it, in the
	// aside, so `form=` by id is the only thing that makes them submit the
	// statement rather than nothing.
	id := regexp.MustCompile(`<form id="([^"]+)"`).FindStringSubmatch(body)
	if id == nil {
		t.Fatal("the editor's form has no id for the tree to point at")
	}
	if !strings.Contains(body, `form="`+id[1]+`" name="expand"`) {
		t.Errorf("the tree's buttons do not submit the form %q", id[1])
	}
}

// CLICKING THE OPEN ONE CLOSES IT, which is what a disclosure does.
func TestOpeningTheSameRelationTwiceClosesIt(t *testing.T) {
	w, ui := browsing(t)
	body := expand(t, ui, probeTarget, "", "sales.daily", "sales.daily")

	if strings.Contains(body, ">sku<") {
		t.Error("the relation stayed open")
	}
	if slices.Contains(w.calls, "/v1/columns") {
		t.Error("closing a relation asked the warehouse")
	}
}

// AND RUNNING A QUERY DOES NOT CLOSE THE TREE. The open relation rides in a
// hidden field, so the screen somebody arranged survives the thing the
// screen is for.
func TestRunningAQueryKeepsTheTreeOpen(t *testing.T) {
	_, ui := browsing(t)
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"target": {probeTarget}, "q": {"SELECT 1"}, "open": {"sales.daily"}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if !strings.Contains(rec.Body.String(), ">sku<") {
		t.Error("running a query closed the relation that was open")
	}
}

// A RELATION THAT CANNOT BE DESCRIBED IS NOT AN ERROR PAGE, for the reason
// the tree itself is not one: `Describer` is optional, `serve` answers 501
// where a dialect lacks it, and the box must keep working either way.
func TestARelationThatCannotBeDescribedStillDrawsTheTree(t *testing.T) {
	w, ui := browsing(t)
	w.status["/v1/columns"] = http.StatusNotImplemented
	w.columns = `{"error":"this warehouse cannot say what a relation holds"}`

	body := expand(t, ui, probeTarget, "SELECT 1", "", "sales.daily")
	if !strings.Contains(body, ">daily<") {
		t.Error("the tree is gone")
	}
	if !strings.Contains(body, `name="q"`) {
		t.Error("the box is gone")
	}
}
