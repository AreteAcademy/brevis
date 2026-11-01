package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// endpointPageFixture is the endpoints fixture with the fresh table's writer
// describing where it reads, and one endpoint reading a gateway's sink.
func endpointPageFixture() ([]postgres.PublishedEndpoint, []postgres.CatalogEntry) {
	all, entries := endpointsFixture()
	entries[0].Writers[0].Config = &postgres.WriterConfig{Fields: []catalog.ManifestField{
		{Path: "source.driver", Value: "from.HTTP"}, {Path: "source.url", Value: "https://api.example.com/v1/orders"},
	}}
	clicks := catalog.Endpoint{Name: "clicks", Path: "/v1/clicks", Tenant: "none", Reads: []string{"pubsub://acme/clicks"}}
	all = append(all, postgres.PublishedEndpoint{API: "metrics", Name: "clicks", Path: "/v1/clicks", Tenant: "none",
		Reads: clicks.Reads, Endpoint: clicks, PublishedAt: clock(9, 20), Manifest: all[0].Manifest})
	return all, entries
}

func endpointNamed(all []postgres.PublishedEndpoint, name string) postgres.PublishedEndpoint {
	for _, e := range all {
		if e.Name == name {
			return e
		}
	}
	panic(name)
}

// WHERE THE DATA COMES FROM IS ONE LINE PER TABLE READ (screen-endpoints
// T4): the writer's declared origin, the writer -- a load or a stream, each
// a link -- and the table with how it stands. A table nobody known writes is
// the table alone.
func TestAnEndpointsLineageIsOneLinePerTable(t *testing.T) {
	all, entries := endpointPageFixture()
	v := BuildEndpoint(endpointNamed(all, "old"), entries, catalogNow)
	if len(v.Lineage) != 2 || v.Lineage[0].Target != "bigquery://acme/bronze/old" || v.Lineage[1].Target != "bigquery://acme/bronze/fresh" {
		t.Fatalf("lineage = %+v", v.Lineage)
	}
	old, fresh := v.Lineage[0], v.Lineage[1]
	if old.Writer == nil || old.WriterHref() != "/ingest/loads/vendors/fetch" || !old.Stale() || old.Source.Kind != "" {
		t.Errorf("the stale table's line: %+v", old)
	}
	if fresh.Stale() || fresh.Source.Kind != "http" || fresh.Source.Where != "https://api.example.com/v1/orders" {
		t.Errorf("the fresh table's line: %+v", fresh)
	}
	if v.Row.Freshness != "stale" {
		t.Errorf("the endpoint stands %q", v.Row.Freshness)
	}

	clicks := BuildEndpoint(endpointNamed(all, "clicks"), entries, catalogNow).Lineage[0]
	if clicks.WriterHref() != "/ingest/streams/edge/v1/clicks" || clicks.Writer.Gateway == nil {
		t.Errorf("a stream's line: %+v", clicks)
	}
	lost := BuildEndpoint(endpointNamed(all, "lost"), entries, catalogNow)
	if len(lost.Lineage) != 1 || lost.Lineage[0].Known || lost.Lineage[0].Writer != nil || lost.Row.Freshness != "" {
		t.Errorf("a table nobody known writes: %+v", lost.Lineage)
	}
}

// AN ENDPOINT SERVING STALE DATA SAYS WHY, above everything else: the
// writer's reason, that the endpoint serves the last good data, and the
// table in the Catalog. One on time says nothing.
func TestAStaleEndpointSaysWhy(t *testing.T) {
	all, entries := endpointPageFixture()
	page := drawIn(t, "en", Endpoint(BuildEndpoint(endpointNamed(all, "old"), entries, catalogNow)))
	if !strings.Contains(page, "data-stale-warning") {
		t.Fatal("no warning")
	}
	warning := page[strings.Index(page, "data-stale-warning"):]
	warning = warning[:strings.Index(warning, "</section>")]
	for _, want := range []string{"vendors", "last good data", `href="/data/target?u=bigquery%3A%2F%2Facme%2Fbronze%2Fold"`} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning lacks %q: %s", want, visibleText(warning))
		}
	}
	if strings.Contains(drawIn(t, "en", Endpoint(BuildEndpoint(endpointNamed(all, "fresh"), entries, catalogNow))), "data-stale-warning") {
		t.Error("an endpoint on time warns")
	}
}

// CLEAR CACHE IS A COMMAND TO COPY (D5), built from what the API published:
// the variable its cache's address is read from, its dialect, and the tables
// read as a build keys them -- with no project on BigQuery, lowered on
// Postgres. A table of another warehouse is not the cache's; with no cache
// on record there is no command.
func TestTheClearCacheCommandIsBuiltFromWhatWasPublished(t *testing.T) {
	all, entries := endpointPageFixture()
	if got := BuildEndpoint(endpointNamed(all, "old"), entries, catalogNow).BumpCommand(); got != "brevis-sql generation bump --generations-from BREVIS_CACHE_ADDR --dialect bigquery bronze.old bronze.fresh" {
		t.Errorf("bigquery: %s", got)
	}
	pg := postgres.PublishedEndpoint{API: "sales", Name: "orders", Path: "/v1/orders",
		Reads: []string{"postgres://db/Public/Invoices", "gs://lake/raw/manual/"},
		Manifest: postgres.APIManifest{Connection: "pg", Dialect: "postgres",
			Cache: &catalog.EndpointsCache{Type: "redis", AddrFrom: "CACHE_ADDR", TTL: "1h"}}}
	if got := BuildEndpoint(pg, nil, catalogNow).BumpCommand(); got != "brevis-sql generation bump --generations-from CACHE_ADDR --dialect postgres public.invoices" {
		t.Errorf("postgres: %s", got)
	}
	if got := BuildEndpoint(endpointNamed(all, "lost"), entries, catalogNow).BumpCommand(); got != "" {
		t.Errorf("no cache on record: %s", got)
	}
}

// THE CACHE CARD SAYS HOW LONG, WHEN IT IS CLEARED AND BY WHAT, and the
// command to clear it by hand; an API published before its settings were
// kept says to publish it again rather than claim there is no cache.
func TestTheCacheCard(t *testing.T) {
	all, entries := endpointPageFixture()
	page := drawIn(t, "en", Endpoint(BuildEndpoint(endpointNamed(all, "fresh"), entries, catalogNow)))
	card := page[strings.Index(page, `id="cache"`):]
	card = card[:strings.Index(card, "</section>")]
	for _, want := range []string{"15 minutes", "Redis", "bronze/fresh", `data-copy="#endpoint-bump"`,
		"brevis-sql generation bump --generations-from BREVIS_CACHE_ADDR --dialect bigquery bronze.fresh"} {
		if !strings.Contains(card, want) {
			t.Errorf("the cache card lacks %q", want)
		}
	}
	lost := drawIn(t, "en", Endpoint(BuildEndpoint(endpointNamed(all, "lost"), entries, catalogNow)))
	card = lost[strings.Index(lost, `id="cache"`):]
	card = card[:strings.Index(card, "</section>")]
	if !strings.Contains(card, "publish the API again") || strings.Contains(card, "generation bump") {
		t.Errorf("an API with no settings on record: %s", visibleText(card))
	}
}

// THE HEAD: back to every endpoint, then GET · API · published · state above
// the path.
func TestAnEndpointsHead(t *testing.T) {
	all, entries := endpointPageFixture()
	page := drawIn(t, "en", Endpoint(BuildEndpoint(endpointNamed(all, "old"), entries, catalogNow)))
	head := page[strings.Index(page, "data-page-header"):strings.Index(page, "<h1")]
	for _, want := range []string{`href="/data/endpoints"`, "All endpoints", ">GET<", "metrics", "published 2h ago", "stale data"} {
		if !strings.Contains(head, want) {
			t.Errorf("the head lacks %q", want)
		}
	}
}

// AN ENDPOINT'S PAGE SPEAKS THE READER'S LANGUAGE; the path, the
// parameters' names and types and the command are the machine's.
func TestAnEndpointsPageHasNoEnglishLeft(t *testing.T) {
	all, entries := endpointPageFixture()
	params := []catalog.EndpointParam{
		{Name: "q", Type: "string", MaxLen: 32}, {Name: "n", Type: "int", Min: rows64(1), Max: rows64(9)},
		{Name: "on", Type: "bool"}, {Name: "day", Type: "date", Required: true},
	}
	old := endpointNamed(all, "old")
	old.Endpoint.Params = params
	var text string
	for _, e := range []postgres.PublishedEndpoint{old, endpointNamed(all, "lost"), endpointNamed(all, "clicks")} {
		page := regexpPre.ReplaceAllString(drawIn(t, "pt-BR", Endpoint(BuildEndpoint(e, entries, catalogNow))), " ")
		text += visibleText(page)
	}
	for _, english := range []string{
		"All endpoints", "published", "stale data", "Where the data comes from", "rebuilt", "last good data",
		"Parameters", "Name", "Accepts", "Default", "required", "text, at most", "a whole number", "true or false",
		"Limits", "Timeout", "Rows", "Bytes billed", "no ceiling", "Each request", "The same rows",
		"Call it", "Time to live", "Cleared when", "Clear cache", "publish the API again", "Open in SQL", "None.",
	} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese page still says %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
