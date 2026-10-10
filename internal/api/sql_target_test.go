package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// THE CONSOLE DOES NOT PROXY A TARGET THE CATALOG DOES NOT KNOW.
//
// `/sql`'s GET path already refuses one -- "a parameter naming anything else
// is ignored rather than trusted" -- and the POST path did not. It read
// `target` straight out of the body and handed it to the service.
//
// What that is worth to somebody signed in: the service answers differently
// for a connection it holds and one it does not, so the body was a way to ask
// `serve` which connections it has been configured with, one guess at a time,
// without the catalog ever being consulted.
//
// The rule is the one `/data/target` already follows by answering 404: a
// destination the catalog does not list is not a destination.
func TestAPostedTargetTheCatalogDoesNotKnowNeverReachesTheService(t *testing.T) {
	w, ui := browsing(t)
	mux := http.NewServeMux()
	ui.Registrar(mux)

	form := url.Values{
		"target": {"bigquery://someone-elses-project/secret/table"},
		"q":      {"SELECT 1"},
	}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	for _, path := range w.calls {
		if path == "/v1/query" {
			t.Fatalf("a target the catalog does not list reached the service; calls: %v", w.calls)
		}
	}
	// AND THE SCREEN SAYS SO rather than rendering as though it had run.
	if body := rec.Body.String(); !strings.Contains(body, "not a destination this console knows") {
		t.Errorf("the screen does not say why nothing ran")
	}
}

// AND THE ONE IT DOES KNOW STILL RUNS, so the guard is a guard and not a
// wall. A refusal that refuses everything passes the test above by doing
// nothing, which is the failure this repository keeps paying for.
func TestAPostedTargetTheCatalogKnowsStillRuns(t *testing.T) {
	w, ui := browsing(t)
	mux := http.NewServeMux()
	ui.Registrar(mux)

	form := url.Values{"target": {probeTarget}, "q": {"SELECT 1"}}
	r := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	var ran bool
	for _, path := range w.calls {
		if path == "/v1/query" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("the catalog's own destination did not run; calls: %v", w.calls)
	}
}
