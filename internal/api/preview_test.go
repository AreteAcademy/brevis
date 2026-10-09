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
	if strings.Contains(body, "Preview") {
		t.Error("the tab drew itself with no result and no reason")
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

// withPreview builds a console holding one destination and, optionally, a
// SQL service.
func withPreview(t *testing.T, c *sqlserve.Client) *api.UI {
	t.Helper()
	detail := &postgres.TargetDetail{
		CatalogEntry: postgres.CatalogEntry{
			Target: probeTarget, Kind: "bigquery",
			Writers: []postgres.CatalogWriter{{
				Workflow: "bronze", Node: "load", LastLoaded: time.Now().Add(-10 * time.Minute),
			}},
		},
		Loads: []postgres.TargetLoad{{
			RunID: uuid.NewString(), Workflow: "bronze", Node: "load",
			LoadedAt: time.Now().Add(-10 * time.Minute),
		}},
	}
	return dataUI(catalogFake{detail: detail}).WithPreview(c)
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
