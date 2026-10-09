package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// THE WHOLE PATH, from a URL to a grid: the handler reads `?tab=preview`,
// asks the service, and the page draws what came back.
func TestTheTabDrawsWhatTheServiceAnswered(t *testing.T) {
	var gotTarget string
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotTarget = string(b)
		_, _ = w.Write([]byte(`{"columns":["k","n"],"rows":[["1",null]],"truncated":true,"limit":20}`))
	}))
	defer svc.Close()

	body := render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/data/target?u="+probeTarget+"&tab=preview")

	if !strings.Contains(gotTarget, probeTarget) {
		t.Errorf("the service was asked for %q", gotTarget)
	}
	for _, want := range []string{"Preview", ">k<", ">n<", "there are more"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// NULL IS DRAWN AS NOTHING-WAS-RECORDED and not as an empty cell.
	if !strings.Contains(body, "—") {
		t.Error("a NULL was drawn as an empty cell")
	}
}

// WITHOUT `?tab=preview` NOTHING IS ASKED. A preview costs a warehouse query,
// and one per page view is a bill for rows nobody looked at.
func TestTheServiceIsNotAskedUnlessTheTabIsOpen(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{"columns":[],"rows":[]}`))
	}))
	defer svc.Close()

	body := render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/data/target?u="+probeTarget)
	if asked {
		t.Error("it queried a warehouse for a page nobody asked a preview of")
	}
	// THE STRIP IS THERE AND NOTHING IS OPEN. The word "Preview" is on the
	// page now because the tab is a LINK -- V1 had no strip at all and this
	// line used to stand in for "the panel did not draw". `aria-current` is
	// what actually says which one is open, so it is what is asserted.
	//
	// COUNTED AND NOT LOOKED FOR, since the top bar marks the open SECTION
	// the same way: one is the nav saying Data, and a second would be a tab.
	if n := strings.Count(body, `aria-current="page"`); n != 1 {
		t.Errorf("%d things claim to be open; only the Data section should", n)
	}
}

// A SERVICE THAT REFUSES STILL RENDERS A PAGE. The reason goes where the grid
// would be; the destination's loads, bars and writers are all still there.
func TestARefusalRendersTheRestOfThePage(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"this target cannot be previewed: only BigQuery destinations can be read today"}`))
	}))
	defer svc.Close()

	body := render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/data/target?u="+probeTarget+"&tab=preview")
	if !strings.Contains(body, "only BigQuery destinations") {
		t.Error("the reason is not on the page")
	}
	if !strings.Contains(body, "Loads") && !strings.Contains(body, "load") {
		t.Error("the rest of the destination page went missing with the preview")
	}
}

// AND AN UNREACHABLE ONE DOES TOO -- with a sentence, never a stack trace.
func TestAnUnreachableServiceStillRendersThePage(t *testing.T) {
	body := render(t, withPreview(t, sqlserve.New("http://127.0.0.1:1", "")),
		"/data/target?u="+probeTarget+"&tab=preview")
	if !strings.Contains(body, "could not be reached") {
		t.Error("the page does not say the service is unreachable")
	}
	if strings.Contains(body, "127.0.0.1:1") {
		t.Error("the address reached the page")
	}
}

// NO SERVICE, NO TAB, and the page is exactly what it was before any of this.
func TestWithNoServiceThePageIsUnchanged(t *testing.T) {
	body := render(t, withPreview(t, nil), "/data/target?u="+probeTarget+"&tab=preview")
	if strings.Contains(body, "Preview") {
		t.Error("a console with no SQL service offered a preview")
	}
}

// probeTarget is the destination every test here asks about.
const probeTarget = "bigquery://acme-prod/bronze/orders"

// withPreview builds a PROTECTED console holding one destination and,
// optionally, a SQL service. openConsole is the same thing with no login,
// which is a console that may not offer the warehouse at all.
func withPreview(t *testing.T, c *sqlserve.Client) *api.UI {
	t.Helper()
	return console(t, c, auth.Credential{User: "ana", Hash: "$2a$10$notarealhash"})
}

func openConsole(t *testing.T, c *sqlserve.Client) *api.UI {
	t.Helper()
	return console(t, c, auth.Credential{})
}

func console(t *testing.T, c *sqlserve.Client, cred auth.Credential) *api.UI {
	t.Helper()
	return consoleOf(t, c, cred, probeTarget, "bigquery")
}

// consoleOf is the same console holding a destination of any shape, which is
// what the refusals need: a bucket and a topic are destinations too.
func consoleOf(t *testing.T, c *sqlserve.Client, cred auth.Credential, target, kind string) *api.UI {
	t.Helper()
	detail := &postgres.TargetDetail{
		CatalogEntry: postgres.CatalogEntry{
			Target: target, Kind: kind,
			Writers: []postgres.CatalogWriter{{
				Workflow: "bronze", Node: "load", LastLoaded: time.Now().Add(-10 * time.Minute),
			}},
		},
		Loads: []postgres.TargetLoad{{
			RunID: uuid.NewString(), Workflow: "bronze", Node: "load",
			LoadedAt: time.Now().Add(-10 * time.Minute),
		}},
	}
	// The LIST as well as the detail: the workbench reads the catalog to
	// offer its connections, and a fake that answered only one destination
	// made it draw "nothing here can be queried yet".
	return dataUI(catalogFake{
		detail:  detail,
		entries: []postgres.CatalogEntry{detail.CatalogEntry, {Target: "file:///data/landing/", Kind: "file"}},
	}).WithPreview(c, cred)
}

func render(t *testing.T, ui *api.UI, path string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s -> %d: %s", path, rec.Code, rec.Body)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}
