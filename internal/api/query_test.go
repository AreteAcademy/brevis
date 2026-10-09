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

// THE TAB, AND ONLY WHERE THERE IS A SERVICE TO ASK. One tab now: a preview
// belongs to a destination and a query belongs to the workbench, which the
// strip links to instead of pretending to hold.
func TestThePreviewTabIsThereWhenAServiceIsAndNotWhenItIsNot(t *testing.T) {
	svc := (&sqlFake{body: `{}`}).start(t)
	with := render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/data/target?u="+probeTarget)
	for _, want := range []string{"tab=preview", "/sql?target="} {
		if !strings.Contains(with, want) {
			t.Errorf("a console with a service offers no %q", want)
		}
	}

	without := render(t, withPreview(t, nil), "/data/target?u="+probeTarget)
	for _, gone := range []string{"tab=preview", "/sql?target="} {
		if strings.Contains(without, gone) {
			t.Errorf("a console with no service still offers %q", gone)
		}
	}
}

// A CONSOLE WITH NO LOGIN DOES NOT OFFER THE WAREHOUSE.
//
// CHECKPOINT B's F4. The console can run with no credential at all, and
// already says so at boot: "interface is OPEN: anyone can trigger a
// workflow". With the Query tab that sentence became incomplete -- it is now
// also anyone can READ EVERY TABLE IN THE PROJECT, because a query is not
// confined to the destination it was opened from, by design.
//
// The tab does not create the hole. It changes what falls through it, and a
// data tool must not outlive the authentication of the screen it sits on.
func TestAnUnprotectedConsoleOffersNoTabs(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{"columns":["k"],"rows":[["1"]]}`))
	}))
	defer svc.Close()
	open := openConsole(t, sqlserve.New(svc.URL, ""))

	body := render(t, open, "/data/target?u="+probeTarget)
	for _, gone := range []string{"tab=preview", "/sql?target="} {
		if strings.Contains(body, gone) {
			t.Errorf("a console with no login offers %q", gone)
		}
	}

	// AND NOT BY HIDING A LINK. Somebody typing the parameter, or following
	// one from a message, must not reach a warehouse either.
	render(t, open, "/data/target?u="+probeTarget+"&tab=preview")
	if asked {
		t.Error("an unprotected console reached the warehouse")
	}
}

// THE PANEL IS INSIDE THE PAGE, not under it.
//
// `@panels(v)` sat AFTER the layout's closing brace, so templ emitted it
// outside `<main>` entirely: past the sidebar, past the user menu, at full
// viewport width. V1 shipped it that way with `@preview(v)` and this carried
// the same line over when it was renamed, which is how a working feature read
// as a broken one for a day.
//
// `</main>` is the anchor because it is what the layout actually closes with,
// and a position test is the only kind that can see this at all -- every
// other assertion in this file passes with the markup in the wrong place.
func TestTheTabsRenderInsideThePageLayout(t *testing.T) {
	svc := (&sqlFake{body: `{"columns":["k"],"rows":[["1"]]}`}).start(t)
	body := render(t, withPreview(t, sqlserve.New(svc.URL, "")),
		"/data/target?u="+probeTarget+"&tab=preview")

	closing := strings.Index(body, "</main>")
	if closing < 0 {
		t.Fatal("the layout no longer closes with </main>; this test needs a new anchor")
	}
	for _, want := range []string{"tab=preview", "/sql?target="} {
		at := strings.Index(body, want)
		if at < 0 {
			t.Errorf("%q is not on the page at all", want)
			continue
		}
		if at > closing {
			t.Errorf("%q renders after </main>, which puts it outside the page", want)
		}
	}
}
