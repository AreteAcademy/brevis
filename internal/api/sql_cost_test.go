package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
)

// THE COST LINE IS A POST, AND THE REASON IS THE ONE THIS CONSOLE REPEATS.
//
// It is the only fragment endpoint here that carries SQL, and a statement in
// a URL is a statement in a proxy log, in a browser's history and in a
// Referer header. The other two take a target and a relation, which is why
// they are GETs.
func TestPricingAStatementIsAPost(t *testing.T) {
	_, ui := browsing(t)
	mux := http.NewServeMux()
	ui.Registrar(mux)

	// 405 AND NOT MERELY "NOT 200". The handler also refuses a GET on its
	// own -- it reads the body and finds no target -- so asserting that the
	// answer is not 200 passes with the route registered as a GET, which is
	// the thing this test is named after. The method pattern is what makes
	// it 405, and 405 is what proves the pattern is still there.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/sql/estimate?target="+url.QueryEscape(probeTarget)+"&q="+url.QueryEscape("SELECT 1"), nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET carrying a statement answered %d, and this endpoint is a POST", rec.Code)
	}

	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)
	if strings.Contains(js, `"/api/sql/estimate?`) {
		t.Error("the island puts the statement in the query string")
	}
}

// A NUMBER, IN THE SENTENCE THE REFERENCE USES.
func TestTheCostLineSaysWhatAStatementWouldProcess(t *testing.T) {
	w, ui := browsing(t)
	w.estimate = `{"bytes":124723}`

	body := price(t, ui, probeTarget, "SELECT 1")
	if !strings.Contains(body, "121.8 kB") {
		t.Errorf("the line does not carry the figure: %s", body)
	}
	if !strings.Contains(body, "will process") {
		t.Errorf("the line does not say what the figure is: %s", body)
	}
	if got := w.asked["/v1/estimate"]["statement"]; got != "SELECT 1" {
		t.Errorf("the service was asked to price %q", got)
	}
}

// A REFUSAL IS SHOWN AS A REFUSAL, WHICH IS THE WHOLE VALUE OF THE LINE.
//
// A statement the service will not run says so HERE, before Run is pressed.
// The sentence is the service's own -- the same one the query would get --
// because a console that reworded it would be a console that drifts from
// what actually happens.
func TestAStatementOverTheCeilingSaysSoBeforeRun(t *testing.T) {
	w, ui := browsing(t)
	w.status["/v1/estimate"] = http.StatusBadRequest
	w.estimate = `{"error":"this query would scan 20 GB, and this service stops at 10 GB. Narrow the columns, or add a filter on a partitioned one."}`

	body := price(t, ui, probeTarget, "SELECT * FROM bronze.everything")
	if !strings.Contains(body, "stops at 10 GB") {
		t.Errorf("the ceiling's refusal never reached the screen: %s", body)
	}
	if !strings.Contains(body, `data-cost-state="refused"`) {
		t.Errorf("a refusal is drawn as a price: %s", body)
	}
}

// AND A WAREHOUSE THAT CANNOT PRICE GETS A SENTENCE, NOT A NUMBER.
//
// Postgres has no Estimator. `EXPLAIN` was refused at CHECKPOINT E: it
// prices in planner units nobody is billed in, and a number in the wrong
// unit under "this query will process" is worse than no number.
func TestAWarehouseThatCannotPriceSaysSo(t *testing.T) {
	w, ui := browsing(t)
	w.status["/v1/estimate"] = http.StatusNotImplemented
	w.estimate = `{"error":"this warehouse does not price a query before it runs"}`

	body := price(t, ui, probeTarget, "SELECT 1")
	if !strings.Contains(body, "does not price") {
		t.Errorf("the screen says nothing about why there is no number: %s", body)
	}
	if strings.Contains(body, "will process") {
		t.Errorf("a warehouse that cannot price produced a price: %s", body)
	}
}

// AND WHEN THE SERVICE IS THE PROBLEM, THE LINE IS SILENT.
//
// A rate limit, a busy service or an unreachable one is an operator's
// problem and not the reader's: it says nothing about the statement in the
// box, and a red line under somebody's half-written SQL that is really about
// a token is a line that teaches them to ignore the line.
func TestTheCostLineIsSilentAboutTheServicesOwnProblems(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusUnauthorized} {
		w, ui := browsing(t)
		w.status["/v1/estimate"] = code
		w.estimate = `{"error":"this service prices at most 120 statements a minute"}`

		mux := http.NewServeMux()
		ui.Registrar(mux)
		rec := httptest.NewRecorder()
		form := url.Values{"target": {probeTarget}, "q": {"SELECT 1"}}
		r := httptest.NewRequest(http.MethodPost, "/api/sql/estimate", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, r)

		if rec.Code == http.StatusOK && strings.TrimSpace(rec.Body.String()) != "" {
			t.Errorf("a %d from the service drew something on the screen: %s", code, rec.Body)
		}
	}
}

// THE PAUSE IS IN THE MARKUP, like every other number this screen's island
// reads: the grips' floor, the rail's cap. A debounce that lived only in
// JavaScript is a number nobody reading the page can find.
func TestTheDebounceIsOnThePage(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")

	line := tag(body, "data-cost")
	if line == "" {
		t.Fatal("there is no cost line on the screen")
	}
	if !strings.Contains(line, `data-estimate-after="600"`) {
		t.Errorf("the line does not say how long it waits: %s", line)
	}
	_, raw := get(t, ui, "/assets/sql.js")
	js := strip(raw)
	for _, want := range []string{"data-estimate-after", "/api/sql/estimate"} {
		if !strings.Contains(js, want) {
			t.Errorf("the island's CODE never uses %q", want)
		}
	}
}

// price asks the console to price one statement, the way the island does.
func price(t *testing.T, ui *api.UI, target, statement string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"target": {target}, "q": {statement}}
	r := httptest.NewRequest(http.MethodPost, "/api/sql/estimate", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/sql/estimate answered %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}
