package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// A LOAD NEVER ASKS THE WAREHOUSE. Its page had a Preview tab, asked only
// when opened; its drawer has none (screen-loads T4) -- "Query destination"
// opens the workbench instead -- so a typed `?tab=preview` asks nothing
// either. A query costs money, and none should hang off a page view.
func TestALoadNeverAsksTheWarehouse(t *testing.T) {
	asked := false
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		_, _ = w.Write([]byte(`{"columns":[],"rows":[]}`))
	}))
	defer svc.Close()

	ui := withPreview(t, sqlserve.New(svc.URL, ""))
	render(t, ui, "/ingest/loads/bronze/load")
	render(t, ui, "/ingest/loads/bronze/load?tab=preview")
	if asked {
		t.Error("it queried a warehouse for a load's drawer")
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
