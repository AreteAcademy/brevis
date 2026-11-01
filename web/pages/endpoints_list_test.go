package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// endpointsFixture is three endpoints over the catalog fixture (a fixed
// 11:20): one reading a table on time, one a stale one, one a table the
// catalog does not know; the first two from an API with a Redis cache, the
// last from one published before its manifest was kept.
func endpointsFixture() ([]postgres.PublishedEndpoint, []postgres.CatalogEntry) {
	redis := postgres.APIManifest{Connection: "warehouse", Dialect: "bigquery",
		Cache: &catalog.EndpointsCache{Type: "redis", AddrFrom: "BREVIS_CACHE_ADDR", TTL: "15m"}}
	ep := func(api, name, path, about string, reads []string, m postgres.APIManifest, params ...catalog.EndpointParam) postgres.PublishedEndpoint {
		e := catalog.Endpoint{Name: name, Path: path, Tenant: "key", Reads: reads, Params: params,
			Notes: map[string]string{"": about}}
		return postgres.PublishedEndpoint{API: api, Name: name, Path: path, Tenant: "key", Reads: reads,
			Endpoint: e, PublishedAt: clock(9, 20), Manifest: m}
	}
	all := []postgres.PublishedEndpoint{
		ep("metrics", "fresh", "/v1/chart/fresh", "Fresh orders per hour.\nMore detail here.", []string{"bigquery://acme/bronze/fresh"}, redis,
			catalog.EndpointParam{Name: "grain", Type: "string", Required: true}, catalog.EndpointParam{Name: "day", Type: "date"}),
		ep("metrics", "old", "/v1/kpi/old", "Vendors, stale.", []string{"bigquery://acme/bronze/old", "bigquery://acme/bronze/fresh"}, redis),
		ep("legacy", "lost", "/v1/lost", "", []string{"postgres://nowhere/public/x"}, postgres.APIManifest{}),
	}
	return all, catalogFixture()
}

func endpointKeys(v EndpointsView) string {
	var out []string
	for _, r := range v.Rows {
		out = append(out, r.Name)
	}
	return strings.Join(out, " ")
}

// THE ENDPOINTS ARE WORST FIRST BY THE DATA THEY READ (screen-endpoints T2):
// an endpoint is as stale as the worst table it reads; one reading only
// tables the catalog does not know has no freshness to say.
func TestTheEndpointsAreWorstFirstByTheDataTheyRead(t *testing.T) {
	all, entries := endpointsFixture()
	v := BuildEndpoints(all, entries, EndpointFilter{}, catalogNow)
	if got := endpointKeys(v); got != "old fresh lost" {
		t.Errorf("rows = %s", got)
	}
	states := map[string]string{}
	for _, r := range v.Rows {
		states[r.Name] = r.Freshness
	}
	if states["old"] != "stale" || states["fresh"] != "fresh" || states["lost"] != "" {
		t.Errorf("states = %v", states)
	}
	if v.Total != 3 || v.APIs != 2 || v.Fresh != 1 || v.Stale != 1 {
		t.Errorf("total %d, apis %d, fresh %d, stale %d", v.Total, v.APIs, v.Fresh, v.Stale)
	}
}

// A ROW SAYS WHAT, FROM WHICH TABLE, HOW LONG IT IS CACHED AND HOW IT
// STANDS: the path and its description, the API, the table it reads with
// when it was rebuilt and by whom, the cache's ttl, the parameters, the
// state and when it was published; it opens the endpoint.
func TestAnEndpointRowSaysWhatAndFromWhere(t *testing.T) {
	all, entries := endpointsFixture()
	page := drawIn(t, "en", EndpointsList(BuildEndpoints(all, entries, EndpointFilter{}, catalogNow)))
	row := page[strings.Index(page, `data-href="/data/endpoints/metrics/fresh"`):]
	row = row[:strings.Index(row, "</tr>")]
	for _, want := range []string{"GET", "/v1/chart/fresh", "Fresh orders per hour.", "metrics",
		`aria-label="Google BigQuery"`, "fresh", "rebuilt", "by orders · load", "TTL 15 minutes", "2 · 1 required", "fresh data", "2h ago"} {
		if !strings.Contains(row, want) {
			t.Errorf("the fresh row has no %q in %s", want, visibleText(row))
		}
	}
	if strings.Contains(row, "More detail here") {
		t.Error("the row shows more than the description's first line")
	}
	old := page[strings.Index(page, `data-href="/data/endpoints/metrics/old"`):]
	old = old[:strings.Index(old, "</tr>")]
	if !strings.Contains(old, "stale data") || !strings.Contains(old, "+1") {
		t.Error("the stale row does not say so, or hides its second table")
	}
	lost := page[strings.Index(page, `data-href="/data/endpoints/legacy/lost"`):]
	lost = lost[:strings.Index(lost, "</tr>")]
	if !strings.Contains(lost, "—") || strings.Contains(lost, "TTL") {
		t.Error("an endpoint with no cache on record claims one")
	}
}

// THE SUMMARY, THE SEARCH AND THE FOOTER: how many endpoints and APIs, fresh
// and stale, the caches and connections published; a search over the path,
// the table and the parameters, on the server; how an endpoint is made.
func TestTheEndpointsSummarySearchAndFoot(t *testing.T) {
	all, entries := endpointsFixture()
	page := drawIn(t, "en", Endpoints(BuildEndpoints(all, entries, EndpointFilter{}, catalogNow)))
	summary := page[strings.Index(page, "data-endpoints-summary"):]
	summary = summary[:strings.Index(summary, "</div>")]
	for _, want := range []string{"3 endpoints", "2 APIs", "1 with fresh data", "1 serving stale data", "cache redis", "connection warehouse (bigquery)"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary has no %q", want)
		}
	}
	for _, want := range []string{`data-swap="endpoints-list"`, "data-how-it-works", "One YAML file per API", "brevis endpoints publish"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	for q, want := range map[string]string{"FRESH": "old fresh", "grain": "fresh", "nowhere": "lost", "/kpi/": "old"} {
		if got := endpointKeys(BuildEndpoints(all, entries, EndpointFilter{Q: q}, catalogNow)); got != want {
			t.Errorf("q=%s draws %q, want %q", q, got, want)
		}
	}
}

// ENDPOINTS SPEAK THE READER'S LANGUAGE (screen-endpoints T2); paths, APIs,
// tables, parameters and the authors' descriptions are the data's own.
func TestTheEndpointsHaveNoEnglishLeft(t *testing.T) {
	all, entries := endpointsFixture()
	text := visibleText(drawIn(t, "pt-BR", Endpoints(BuildEndpoints(all, entries, EndpointFilter{}, catalogNow)))) + " " +
		visibleText(drawIn(t, "pt-BR", EndpointsList(BuildEndpoints(nil, nil, EndpointFilter{}, catalogNow)))) + " " +
		visibleText(drawIn(t, "pt-BR", EndpointsList(BuildEndpoints(all, entries, EndpointFilter{Q: "zzz"}, catalogNow))))
	for _, english := range []string{
		// "endpoint" and "cache" are the product's words in every language.
		"Metrics published", "read-only", "with fresh data", "serving stale", "connection",
		"Reads", "Parameters", "Status", "Published", "rebuilt", "required", "fresh data", "stale data",
		"no freshness", "How it works", "One YAML", "Nothing matches", "No endpoint", "Search", "hours ago", "minutes",
	} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese endpoints still say %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
