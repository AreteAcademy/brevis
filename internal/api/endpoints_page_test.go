package api_test

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/endpointcall"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

type endpointsFake struct{ all []postgres.PublishedEndpoint }

func (f endpointsFake) List(context.Context) ([]postgres.PublishedEndpoint, error) { return f.all, nil }
func (f endpointsFake) Get(_ context.Context, api, name string) (postgres.PublishedEndpoint, bool, error) {
	for _, e := range f.all {
		if e.API == api && e.Name == name {
			return e, true, nil
		}
	}
	return postgres.PublishedEndpoint{}, false, nil
}
func (f endpointsFake) Reading(_ context.Context, target string) ([]postgres.PublishedEndpoint, error) {
	var out []postgres.PublishedEndpoint
	for _, e := range f.all {
		for _, r := range e.Reads {
			if r == target {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func published() endpointsFake {
	def := "last_7_days"
	clicks := catalog.Endpoint{Name: "clicks", Path: "/v1/clicks", Tenant: "key",
		Params: []catalog.EndpointParam{
			{Name: "period", Type: "period", Default: &def},
			{Name: "order", Type: "enum", Values: []string{"asc", "desc"}},
		},
		Limits: catalog.EndpointLimits{Timeout: "10s", MaxRows: 500, MaxBytesBilled: 1 << 30},
		Reads:  []string{"postgres://analytics/marts/clicks_daily"},
		Notes:  map[string]string{"": "Clicks per day, for the dashboard.", "params.order": "Newest first by default."}}
	return endpointsFake{all: []postgres.PublishedEndpoint{{
		API: "metrics-api", Name: "clicks", Path: "/v1/clicks", Tenant: "key",
		Reads: clicks.Reads, Endpoint: clicks, PublishedAt: time.Now().Add(-2 * time.Hour),
	}}}
}

func TestTheEndpointsPageListsWhatWasPublished(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(published())
	code, body := get(t, ui, "/data/endpoints")
	if code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	for _, want := range []string{"/v1/clicks", "metrics-api", "Clicks per day, for the dashboard.",
		"postgres://analytics/marts/", "clicks_daily", "/data/endpoints/metrics-api/clicks"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
}

// AN ENDPOINT'S PAGE IS ITS CONTRACT: parameters with types and defaults,
// limits, the models it reads, the author's notes, a curl with the key as a
// placeholder. NO TRAFFIC: that is the API's /metrics, live.
func TestAnEndpointsPageIsItsContract(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(published())
	code, body := get(t, ui, "/data/endpoints/metrics-api/clicks")
	if code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	for _, want := range []string{"last_7_days", "asc, desc", "500", "1.0 GB", "10s",
		"Clicks per day, for the dashboard.", "Newest first by default.", "Bearer &lt;key&gt;",
		"/data/target?u=postgres%3A%2F%2Fanalytics%2Fmarts%2Fclicks_daily", "each key's tenant"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if code, _ := get(t, ui, "/data/endpoints/metrics-api/nope"); code != http.StatusNotFound {
		t.Errorf("an unpublished endpoint answered %d", code)
	}
}

// THE MODEL'S PAGE NAMES WHO READS IT, beside who writes it: the edge the
// endpoint adds to its lineage.
func TestAModelsPageListsTheEndpointsThatReadIt(t *testing.T) {
	target := "postgres://analytics/marts/clicks_daily"
	model := postgres.CatalogEntry{Target: target, Kind: "postgres",
		Writers: []postgres.CatalogWriter{{Workflow: "marts", Node: "build", LastLoaded: time.Now()}}}
	ui := dataUI(catalogFake{detail: &postgres.TargetDetail{CatalogEntry: model}, entries: []postgres.CatalogEntry{model}}).
		WithEndpoints(published())
	code, body := get(t, ui, "/ingest/loads/marts/build")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, "Read by") || !strings.Contains(body, "/data/endpoints/metrics-api/clicks") {
		t.Error("the model's page does not list the endpoint over it")
	}
}

func TestWithNothingPublishedThePageSaysHow(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{}).WithEndpoints(endpointsFake{}), "/data/endpoints")
	if code != http.StatusOK || !strings.Contains(body, "brevis endpoints publish") {
		t.Errorf("%d %s", code, body)
	}
}

func tryUI(t *testing.T, apiURL string, cred auth.Credential) *api.UI {
	t.Helper()
	calls, err := endpointcall.New("metrics-api="+apiURL, "console-key")
	if err != nil {
		t.Fatal(err)
	}
	return dataUI(catalogFake{}).WithEndpoints(published()).WithEndpointCalls(calls, cred)
}

var loggedIn = auth.Credential{User: "ana", Hash: "$2a$10$notarealhash"}

// TRY IT: a form with one field per parameter, typed as the parameter is,
// and nothing asked of the API until Run is pressed -- a call is a warehouse
// query, and one per page view is a bill for rows nobody looked at.
func TestAnEndpointsPageOffersAFormAndAsksNothingUntilRun(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked = true }))
	defer srv.Close()
	_, body := get(t, tryUI(t, srv.URL, loggedIn), "/data/endpoints/metrics-api/clicks")
	for _, want := range []string{"Try it", `name="run"`, `name="period"`, `<select`, `<option value="desc"`, "console's key"} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if asked {
		t.Error("the API was called for a page nobody pressed Run on")
	}
}

// RUN: the declared parameters go to the API as given, the console's key
// with them, and the rows come back as a grid with the status, the cache
// and the time beside it.
func TestRunCallsTheAPIAndDrawsWhatItAnswered(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("X-Brevis-Cache", "miss")
		_, _ = w.Write([]byte(`{"data":[{"day":"2026-10-12","clicks":412}],"truncated":false,"meta":{"rows":1}}`))
	}))
	defer srv.Close()
	code, body := get(t, tryUI(t, srv.URL, loggedIn), "/data/endpoints/metrics-api/clicks?run=1&period=today&order=&nope=x")
	if code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	if got == nil || got.URL.Path != "/v1/clicks" || got.URL.Query().Get("period") != "today" ||
		got.URL.Query().Has("order") || got.URL.Query().Has("nope") || got.URL.Query().Has("run") ||
		got.Header.Get("Authorization") != "Bearer console-key" {
		t.Fatalf("the API was asked %v", got)
	}
	for _, want := range []string{">day<", ">clicks<", ">412<", "200", "miss"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer lacks %q", want)
		}
	}
	// the form keeps what was sent, so the next run starts from it
	if !strings.Contains(body, `<option value="today" selected`) {
		t.Error("the form forgot the period that was run")
	}
}

func TestARefusalIsShownInTheAPIsWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"the parameter period is one of: today, yesterday"}`))
	}))
	defer srv.Close()
	_, body := get(t, tryUI(t, srv.URL, loggedIn), "/data/endpoints/metrics-api/clicks?run=1&period=nope")
	if !strings.Contains(body, "400") || !strings.Contains(body, "the parameter period is one of: today, yesterday") {
		t.Error("the refusal is not on the page")
	}
}

// ONLY ON A CONSOLE THAT ASKS WHO YOU ARE, and only for an API it has an
// address for. The key is one tenant's for every login: an open console
// would hand that tenant's rows to whoever found the address.
func TestTryItNeedsALoginAndAnAddress(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked = true }))
	defer srv.Close()
	_, body := get(t, tryUI(t, srv.URL, auth.Credential{}), "/data/endpoints/metrics-api/clicks?run=1&period=today")
	if strings.Contains(body, "Try it") || asked {
		t.Error("an open console offered Try it, or called the API")
	}
	other, _ := endpointcall.New("another-api="+srv.URL, "")
	_, body = get(t, dataUI(catalogFake{}).WithEndpoints(published()).WithEndpointCalls(other, loggedIn), "/data/endpoints/metrics-api/clicks")
	if strings.Contains(body, "Try it") {
		t.Error("Try it was offered for an API with no address")
	}
}

func TestAnAnswerLongerThanThePageSaysHowManyThereWere(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := make([]string, 600)
		for i := range rows {
			rows[i] = `{"k":1}`
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(rows, ",") + `]}`))
	}))
	defer srv.Close()
	_, body := get(t, tryUI(t, srv.URL, loggedIn), "/data/endpoints/metrics-api/clicks?run=1")
	if !strings.Contains(body, "the first 500 of 600 rows") {
		t.Error("the page does not say it drew a part of the answer")
	}
}

// twoAPIs is two endpoints on two APIs, reading different models.
func twoAPIs() endpointsFake {
	f := published()
	req := catalog.Endpoint{Name: "orders", Path: "/v1/orders", Tenant: "key",
		Params: []catalog.EndpointParam{{Name: "day", Type: "date", Required: true}},
		Reads:  []string{"postgres://warehouse/sales/orders", "postgres://warehouse/sales/customers"}}
	f.all = append(f.all, postgres.PublishedEndpoint{API: "sales-api", Name: "orders", Path: "/v1/orders",
		Tenant: "key", Reads: req.Reads, Endpoint: req, PublishedAt: time.Now().Add(-time.Hour)})
	return f
}

// THE LIST IS AN INDEX: method and path, the API, how many parameters (and
// how many required), what it reads and when it was published -- and the
// whole row goes to the endpoint's page.
func TestTheEndpointsListIsAnIndex(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{}).WithEndpoints(twoAPIs()), "/data/endpoints")
	for _, want := range []string{">GET<", ">/v1/orders<", "sales-api", `data-href="/data/endpoints/sales-api/orders"`,
		"postgres://warehouse/sales/", ">orders<", "+1", "1 · 1 required", "2 endpoints", "2 APIs"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %s", want)
		}
	}
}

// THE LIST JUDGES AN ENDPOINT BY THE CATALOG (screen-endpoints T2): one
// reading a stale table says so, with when that table was rebuilt and by
// whom; one reading a table the catalog does not know says nothing.
func TestTheEndpointsListReadsTheCatalog(t *testing.T) {
	f := published()
	stale := catalog.Endpoint{Name: "vendors", Path: "/v1/vendors", Tenant: "key", Reads: []string{"bigquery://acme/bronze/old"}}
	f.all = append(f.all, postgres.PublishedEndpoint{API: "metrics-api", Name: "vendors", Path: "/v1/vendors",
		Tenant: "key", Reads: stale.Reads, Endpoint: stale, PublishedAt: time.Now().Add(-time.Hour)})
	_, body := get(t, dataUI(catalogFake{entries: catalogEntries()}).WithEndpoints(f), "/data/endpoints")
	if !strings.Contains(body, "1 serving stale data") {
		t.Error("the summary does not count the endpoint serving stale data")
	}
	row := body[strings.Index(body, `data-href="/data/endpoints/metrics-api/vendors"`):]
	row = row[:strings.Index(row, "</tr>")]
	for _, want := range []string{"stale data", "rebuilt 5h ago by vendors"} {
		if !strings.Contains(row, want) {
			t.Errorf("the stale endpoint's row lacks %q", want)
		}
	}
	if strings.Index(body, "/v1/vendors") > strings.Index(body, ">/v1/clicks<") {
		t.Error("the stale endpoint is not first")
	}
}

// THE LIST SEARCHES PATH, API, PARAMETERS AND READS, on the server and while
// somebody types -- the same contract as the catalog's: the box swaps the
// list for the answer fragment.
func TestTheEndpointsListSearches(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(twoAPIs())
	_, body := get(t, ui, "/data/endpoints?q=CUSTOMERS")
	if !strings.Contains(body, ">/v1/orders<") || strings.Contains(body, ">/v1/clicks<") {
		t.Error("?q=CUSTOMERS did not narrow to the endpoint that reads customers")
	}
	for _, want := range []string{`value="CUSTOMERS"`, `data-swap="endpoints-list"`, `id="endpoints-list"`, `id="endpoint-rows"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the searched list lacks %s", want)
		}
	}
	_, answer := fragmentGet(t, ui, "/data/endpoints?q=nothing-like-it")
	if strings.Contains(answer, "<html") || strings.Contains(answer, `role="search"`) {
		t.Error("the answer is the whole page, not the list")
	}
	if !strings.Contains(answer, "Nothing matches this filter.") {
		t.Errorf("a search that finds nothing does not say so: %s", answer)
	}
}

// AN ENDPOINT IS TITLED BY ITS PATH, and what it is goes above: the method,
// the API it belongs to, when it was published.
func TestAnEndpointIsTitledByItsPath(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{}).WithEndpoints(published()), "/data/endpoints/metrics-api/clicks")
	h1 := body[strings.Index(body, "<h1"):]
	h1 = h1[:strings.Index(h1, "</h1>")]
	if !strings.Contains(h1, "/v1/clicks") || !strings.Contains(h1, "font-mono") {
		t.Errorf("the title is not the path in mono: %s", h1)
	}
	eyebrow := body[strings.Index(body, "data-page-header"):strings.Index(body, "<h1")]
	for _, want := range []string{">GET<", "metrics-api", "published"} {
		if !strings.Contains(eyebrow, want) {
			t.Errorf("the line above the title lacks %s", want)
		}
	}
}

// THE LIMITS ARE A STRIP OF NUMBERS, and the curl is a block to copy -- with
// the key as a placeholder, never the console's own.
func TestAnEndpointsLimitsAreNumbersAndItsCurlIsCopyable(t *testing.T) {
	_, body := get(t, tryUI(t, "http://api.invalid", loggedIn), "/data/endpoints/metrics-api/clicks")
	limits := body[strings.Index(body, ">Limits<"):]
	if !strings.Contains(limits[:strings.Index(limits, "</dl>")], "<dt") {
		t.Error("the limits are not a strip of labelled numbers")
	}
	if !strings.Contains(body, `data-copy="#endpoint-curl"`) || !strings.Contains(body, `id="endpoint-curl"`) {
		t.Error("the curl cannot be copied")
	}
	if strings.Contains(body, "console-key") {
		t.Error("the page renders the console's key")
	}
}

// THE DOCS ARE THE PAGE AND TRY IT SITS BESIDE THEM -- after them in the
// document, so a narrow screen reads the contract before the form. Where
// this console cannot run the endpoint there is no side column at all:
// TestTryItNeedsALoginAndAnAddress holds that nothing is offered.
func TestTryItSitsBesideTheDocs(t *testing.T) {
	_, body := get(t, tryUI(t, "http://api.invalid", loggedIn), "/data/endpoints/metrics-api/clicks")
	side := strings.Index(body, "data-side")
	if side < 0 {
		t.Fatal("the endpoint has no side column")
	}
	if p := strings.Index(body, ">Parameters<"); p < 0 || p > side {
		t.Error("the parameters are not in the main column")
	}
	if !strings.Contains(body[side:], ">Try it<") || !strings.Contains(body[side:], `name="run"`) {
		t.Error("Try it is not in the side column")
	}

	_, body = get(t, dataUI(catalogFake{}).WithEndpoints(published()), "/data/endpoints/metrics-api/clicks")
	if strings.Contains(body, "data-side") || strings.Contains(body, `name="run"`) {
		t.Error("a console that cannot run the endpoint still draws a side column for it")
	}
}

// fragmentGet is a GET asking for one part of the page alone, as ui.js does
// for a form with `data-swap`.
func fragmentGet(t *testing.T, ui *api.UI, path string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Brevis-Fragment", "answer")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// A RUN OF TRY IT CAN ASK FOR THE ANSWER ALONE: the status, the time and the
// rows, with nothing around them -- the form and the docs stay as they are,
// and the API is asked once.
func TestTryItCanAnswerAlone(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":[{"day":"2026-10-12","clicks":412}],"truncated":false,"meta":{"rows":1}}`))
	}))
	defer srv.Close()
	code, body := fragmentGet(t, tryUI(t, srv.URL, loggedIn), "/data/endpoints/metrics-api/clicks?run=1&period=today")
	if code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	for _, want := range []string{">412<", ">200<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer lacks %s", want)
		}
	}
	for _, gone := range []string{"<html", `name="run"`, ">Parameters<"} {
		if strings.Contains(body, gone) {
			t.Errorf("the answer carries %s, which is the page around it", gone)
		}
	}
	if calls != 1 {
		t.Errorf("the API was asked %d times for one run", calls)
	}
}

// THE FORM SWAPS ITS ANSWER AND SAYS ITS SHORTCUT: `data-swap` names the pane
// the answer goes into, Run is the primary button with ⌘↵ beside it, and the
// pane says when it is busy. Without a script, the form is a GET that draws
// the whole page with the answer in it, as before.
func TestTryItSwapsItsAnswer(t *testing.T) {
	_, body := get(t, tryUI(t, "http://api.invalid", loggedIn), "/data/endpoints/metrics-api/clicks")
	for _, want := range []string{`data-swap="endpoint-answer"`, `id="endpoint-answer"`, `aria-live="polite"`, "⌘↵", "Run request"} {
		if !strings.Contains(body, want) {
			t.Errorf("Try it lacks %s", want)
		}
	}
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{"form[data-swap]", `"X-Brevis-Fragment": "answer"`, ".redirected", "replaceState", "requestSubmit"} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js's swapping form never uses %s", want)
		}
	}
}

// THE CURL IS THE RUN, AS A COMMAND: what was sent is in it, so copying it
// reproduces the request outside the console. Only the parameters given --
// an empty field is the default and says nothing -- and the key stays a
// placeholder.
func TestTheCurlCarriesTheRunsParameters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"truncated":false,"meta":{"rows":0}}`))
	}))
	defer srv.Close()
	_, body := get(t, tryUI(t, srv.URL, loggedIn), "/data/endpoints/metrics-api/clicks?run=1&period=today&order=")
	curl := html.UnescapeString(body[strings.Index(body, `id="endpoint-curl"`):])
	curl = curl[:strings.Index(curl, "</pre>")]
	if !strings.Contains(curl, "<api address>/v1/clicks?period=today'") {
		t.Errorf("the curl does not carry the run: %s", curl)
	}
	if strings.Contains(curl, "order=") || strings.Contains(curl, "run=") || strings.Contains(curl, "console-key") {
		t.Errorf("the curl carries what it should not: %s", curl)
	}
}

// AND IT FOLLOWS THE FORM AS SOMEBODY TYPES. The block names the form it
// follows and carries its fixed parts; ui.js rebuilds the query string with
// the same escaping Go's url.QueryEscape uses -- a quote typed into a field
// must not end the shell's quoted string.
func TestTheCurlFollowsTheForm(t *testing.T) {
	_, body := get(t, tryUI(t, "http://api.invalid", loggedIn), "/data/endpoints/metrics-api/clicks")
	for _, want := range []string{`id="try-it"`, `data-curl-from="try-it"`, `data-curl-base="&lt;api address&gt;/v1/clicks"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{"[data-curl-from", "encodeURIComponent", `/[!'()*]/g`} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js's curl never uses %s", want)
		}
	}
}

// THE LIST FILTERS BY API AND STATE IN THE URL (screen-endpoints T3), on the
// page and in the answer the search box swaps in.
func TestTheEndpointsListFiltersByAPIAndState(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(twoAPIs())
	_, body := get(t, ui, "/data/endpoints?api=sales-api")
	if !strings.Contains(body, ">/v1/orders<") || strings.Contains(body, ">/v1/clicks<") {
		t.Error("?api=sales-api did not keep sales-api's endpoint alone")
	}
	if !strings.Contains(body, `<input type="hidden" name="api" value="sales-api"`) {
		t.Error("the search box forgets the API chosen")
	}
	_, answer := fragmentGet(t, ui, "/data/endpoints?api=metrics-api&q=clicks")
	if !strings.Contains(answer, ">/v1/clicks<") || strings.Contains(answer, ">/v1/orders<") {
		t.Error("the answer does not keep the API filter")
	}
	_, stale := get(t, ui, "/data/endpoints?state=stale")
	if strings.Contains(stale, ">/v1/clicks<") || !strings.Contains(stale, "Nothing matches this filter.") {
		t.Error("?state=stale drew an endpoint whose data nobody knows to be stale")
	}
}
