package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func noEvents(t *testing.T, pool *postgres.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `TRUNCATE events`); err != nil {
		t.Fatal(err)
	}
}

// sameDetail says whether got has exactly want's keys and values.
func sameDetail(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// A STREAM IS IN THE FEED WHEN IT IS NEW OR ITS CONFIGURATION CHANGED -- the
// rule its configuration history already follows. A republish that changes
// nothing records nothing.
func TestPublishingAStreamIsRecordedWhenNewOrChanged(t *testing.T) {
	pool := gatewaysDB(t)
	noEvents(t, pool)
	repo := postgres.NewGatewayRepo(pool)
	ctx := context.Background()
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	clicks := catalog.ManifestStream{Path: "/v1/clicks", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "pubsub", Target: target("pubsub://acme/clicks")},
	}}

	for i, m := range []catalog.Manifest{
		v2(configured("1m", nil)),         // new
		v2(configured("1m", nil)),         // the same
		v2(configured("2m", nil)),         // changed
		v2(configured("2m", nil), clicks), // one new beside it, with no configuration
		v2(configured("2m", nil), clicks), // the same
	} {
		if err := repo.Publish(ctx, m, at.Add(time.Duration(i)*time.Hour), "cli"); err != nil {
			t.Fatal(err)
		}
	}
	if got := recorded(t, pool); got != "stream.published edge/v1/tables cli, stream.published edge/v1/tables cli, stream.published edge/v1/clicks cli" {
		t.Fatalf("events = %s", got)
	}
	want := []map[string]any{
		{"gateway": "edge", "path": "/v1/tables", "new": true},
		{"gateway": "edge", "path": "/v1/tables", "new": false},
		{"gateway": "edge", "path": "/v1/clicks", "new": true},
	}
	for i, d := range details(t, pool) {
		if !sameDetail(d, want[i]) {
			t.Errorf("event %d: detail %v; want %v", i, d, want[i])
		}
	}
}

// TAKING A GATEWAY DOWN is in the feed with how many streams went; taking
// down one that was never published records nothing.
func TestUnpublishingAGatewayIsRecorded(t *testing.T) {
	pool := gatewaysDB(t)
	repo := postgres.NewGatewayRepo(pool)
	ctx := context.Background()
	if err := repo.Publish(ctx, v2(configured("1m", nil)), time.Now(), "cli"); err != nil {
		t.Fatal(err)
	}
	noEvents(t, pool)

	if _, err := repo.Unpublish(ctx, "edge", "ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Unpublish(ctx, "never_published", "ana"); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, pool); got != "gateway.unpublished edge ana" {
		t.Fatalf("events = %s", got)
	}
	if d := details(t, pool)[0]; !sameDetail(d, map[string]any{"streams": float64(1)}) {
		t.Errorf("detail = %v", d)
	}
}

// AN API'S PUBLISH IS ONE EVENT, when any endpoint is new, changed or gone,
// with how many changed of how many it has now.
func TestPublishingEndpointsIsRecordedWhenSomethingChanged(t *testing.T) {
	pool := endpointsDB(t)
	noEvents(t, pool)
	repo := postgres.NewEndpointRepo(pool)
	ctx := context.Background()
	clicks := endpoint("clicks", "/v1/clicks")
	moved := endpoint("clicks", "/v2/clicks")
	orders := endpoint("orders", "/v1/orders")

	for _, m := range []catalog.Endpoints{
		api("metrics", clicks),         // new: 1 of 1
		api("metrics", clicks),         // the same
		api("metrics", clicks, orders), // one added: 1 of 2
		api("metrics", moved, orders),  // one changed: 1 of 2
		api("metrics", orders),         // one gone: 1 of 1
		api("metrics", orders),         // the same
	} {
		if err := repo.Publish(ctx, m, time.Now(), "cli"); err != nil {
			t.Fatal(err)
		}
	}
	if got := recorded(t, pool); got != "endpoints.published metrics cli, endpoints.published metrics cli, endpoints.published metrics cli, endpoints.published metrics cli" {
		t.Fatalf("events = %s", got)
	}
	want := []map[string]any{
		{"changed": float64(1), "total": float64(1)},
		{"changed": float64(1), "total": float64(2)},
		{"changed": float64(1), "total": float64(2)},
		{"changed": float64(1), "total": float64(1)},
	}
	for i, d := range details(t, pool) {
		if !sameDetail(d, want[i]) {
			t.Errorf("event %d: detail %v; want %v", i, d, want[i])
		}
	}
}

// TAKING AN API DOWN is in the feed with how many endpoints went.
func TestUnpublishingEndpointsIsRecorded(t *testing.T) {
	pool := endpointsDB(t)
	repo := postgres.NewEndpointRepo(pool)
	ctx := context.Background()
	if err := repo.Publish(ctx, api("metrics", endpoint("a", "/a"), endpoint("b", "/b")), time.Now(), "cli"); err != nil {
		t.Fatal(err)
	}
	noEvents(t, pool)

	if _, err := repo.Unpublish(ctx, "metrics", "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Unpublish(ctx, "never_published", "cli"); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, pool); got != "endpoints.unpublished metrics cli" {
		t.Fatalf("events = %s", got)
	}
	if d := details(t, pool)[0]; !sameDetail(d, map[string]any{"endpoints": float64(2)}) {
		t.Errorf("detail = %v", d)
	}
}
