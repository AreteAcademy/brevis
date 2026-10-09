package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// sqlFake is a `brevis-sql serve` that remembers what it was sent.
type sqlFake struct {
	asked  map[string]any
	status int
	body   string
}

func (f *sqlFake) start(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.asked)
		if f.status == 0 {
			f.status = http.StatusOK
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(s.Close)
	return s
}

// run submits the Query form, the way a browser does.
func run(t *testing.T, ui *api.UI, statement string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	form := url.Values{"q": {statement}}
	r := httptest.NewRequest(http.MethodPost,
		"/data/target?u="+probeTarget+"&tab=query", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("the form answered %d: %s", rec.Code, rec.Body)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

// THE WHOLE PATH, from a typed statement to a grid: the form posts, the
// console forwards, and the page draws what came back -- with what it cost.
func TestTheQueryTabRunsAStatementAndDrawsTheGrid(t *testing.T) {
	f := &sqlFake{body: `{"columns":["k","total"],"rows":[["1",42],["2",null]],
		"truncated":false,"limit":100,"bytes":5242880,"ms":310}`}
	svc := f.start(t)

	body := run(t, withPreview(t, sqlserve.New(svc.URL, "")),
		"SELECT k, sum(v) AS total FROM bronze.orders GROUP BY k")

	if got, _ := f.asked["statement"].(string); !strings.Contains(got, "sum(v)") {
		t.Errorf("the service was sent %q", got)
	}
	if got, _ := f.asked["target"].(string); got != probeTarget {
		t.Errorf("the service was sent the target %q", got)
	}
	for _, want := range []string{">k<", ">total<", "42", "5.0 MB", "310 ms"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// NULL IS NOT THE EMPTY STRING, here as everywhere else on this page.
	if !strings.Contains(body, "—") {
		t.Error("a NULL was drawn as an empty cell")
	}
}

// THE STATEMENT NEVER REACHES A URL. A query in a URL is a query in a proxy
// log, in a browser's history and in a Referer header -- and a WHERE clause
// carries customer data. The form is a POST for that reason, and the
// session cookie's SameSite=Lax is what makes a POST the safe one to use.
func TestTheFormPostsAndTheStatementIsNotInTheLink(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	body := render(t, withPreview(t, sqlserve.New(svc.URL, "")),
		"/data/target?u="+probeTarget+"&tab=query")

	if !strings.Contains(body, `method="post"`) {
		t.Error("the Query form is not a POST")
	}
	if strings.Contains(body, "?q=") || strings.Contains(body, "&q=") {
		t.Error("a statement is carried in a link")
	}
}

// A STATEMENT COMES BACK INTO THE BOX. A query refused for a typo, with the
// box emptied, is a query somebody has to type again to fix.
func TestTheStatementIsEchoedBackIntoTheBox(t *testing.T) {
	f := &sqlFake{status: http.StatusBadRequest,
		body: `{"error":"this service only reads, and this is a DELETE statement"}`}
	svc := f.start(t)

	body := run(t, withPreview(t, sqlserve.New(svc.URL, "")), "DELETE FROM bronze.orders")

	if !strings.Contains(body, "DELETE FROM bronze.orders") {
		t.Error("the statement was not put back in the box")
	}
	if !strings.Contains(body, "this is a DELETE statement") {
		t.Error("the service's reason is not on the page")
	}
}

// A REFUSAL IS A SENTENCE WHERE THE GRID WOULD BE, not an error page. The
// page still has its writers, its loads and its tabs: somebody who mistyped a
// column has not lost the screen they were working on.
func TestARefusalKeepsTheScreen(t *testing.T) {
	f := &sqlFake{status: http.StatusBadRequest,
		body: `{"error":"this query would scan 4 TB, and this service stops at 10 GB"}`}
	svc := f.start(t)

	body := run(t, withPreview(t, sqlserve.New(svc.URL, "")), "SELECT * FROM bronze.orders")

	for _, want := range []string{"would scan 4 TB", "Writers", probeTarget} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lost %q", want)
		}
	}
}

// AN EMPTY BOX ASKS NOTHING. A warehouse query for a submit somebody made by
// pressing enter is a bill for a mistake.
func TestAnEmptyStatementAsksNothing(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer svc.Close()

	run(t, withPreview(t, sqlserve.New(svc.URL, "")), "   \n\t ")
	if asked {
		t.Error("an empty box reached the warehouse")
	}
}

// BOTH TABS, AND ONLY WHERE THERE IS A SERVICE TO ASK. A tab that always
// answers "not configured" is a question nobody can act on.
func TestTheTabsAreThereWhenAServiceIsAndNotWhenItIsNot(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	with := render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/data/target?u="+probeTarget)
	for _, want := range []string{"tab=preview", "tab=query"} {
		if !strings.Contains(with, want) {
			t.Errorf("a console with a service offers no %q", want)
		}
	}

	without := render(t, withPreview(t, nil), "/data/target?u="+probeTarget)
	for _, gone := range []string{"tab=preview", "tab=query"} {
		if strings.Contains(without, gone) {
			t.Errorf("a console with no service still offers %q", gone)
		}
	}
}

// OPENING THE TAB ASKS NOTHING. The query runs when somebody runs it, which
// is the difference between a Query tab and a preview with a text box.
func TestOpeningTheQueryTabRunsNothing(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer svc.Close()

	render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/data/target?u="+probeTarget+"&tab=query")
	if asked {
		t.Error("opening the tab ran a query")
	}
}

// A STATEMENT IN THE URL IS NOT A STATEMENT. This is the property the POST
// form exists for, and it holds against a link somebody crafted rather than
// against the form the page draws: `?q=…` is ignored, so a query cannot be
// put in a browser's history, a proxy log or a Referer header by anybody --
// including by a page on another site linking here.
//
// It is `PostFormValue` that does it, by reading the body and never the
// query string. The method check in the handler is the readable statement of
// the same intent; this is what makes the intent true.
func TestAStatementInTheURLIsIgnored(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer svc.Close()
	ui := withPreview(t, sqlserve.New(svc.URL, ""))

	render(t, ui, "/data/target?u="+probeTarget+"&tab=query&q="+url.QueryEscape("SELECT 1"))
	if asked {
		t.Error("a statement in the URL was run")
	}

	// AND NOT EVEN WHEN THE REQUEST IS A POST. The body is the only place a
	// statement is read from.
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost,
		"/data/target?u="+probeTarget+"&tab=query&q="+url.QueryEscape("SELECT 1"), strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, r)
	if asked {
		t.Error("a POST took its statement from the URL")
	}
}
