package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
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
	for _, want := range []string{"/v1/clicks", "metrics-api", "period", "order",
		"postgres://analytics/marts/clicks_daily", "/data/endpoints/metrics-api/clicks"} {
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
	ui := dataUI(catalogFake{detail: &postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{Target: target}}}).
		WithEndpoints(published())
	code, body := get(t, ui, "/data/target?u="+target)
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
