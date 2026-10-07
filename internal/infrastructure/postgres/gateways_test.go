package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func gatewaysDB(t *testing.T) *postgres.Pool {
	t.Helper()
	pool := loadDB(t)
	if _, err := pool.Exec(context.Background(), `TRUNCATE gateway_destinations`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func target(s string) *string { return &s }

func manifest(streams ...catalog.ManifestStream) catalog.Manifest {
	return catalog.Manifest{Kind: "gateway-manifest", Version: 1, Gateway: "edge", Streams: streams}
}

func countRows(t *testing.T, pool *postgres.Pool, gw string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM gateway_destinations WHERE gateway = $1`, gw).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Publishing replaces the gateway's rows: twice is the same, and a stream
// removed from the config is gone after the next publish.
func TestPublishingAGatewayReplacesItsRows(t *testing.T) {
	pool := gatewaysDB(t)
	ctx := context.Background()
	repo := postgres.NewGatewayRepo(pool)
	at := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)

	clicks := catalog.ManifestStream{Path: "/v1/clicks", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "pubsub", Target: target("pubsub://acme/clicks")},
		{Role: "dead_letter", Kind: "files", Note: "relative path"},
	}}
	tables := catalog.ManifestStream{Path: "/v1/tables", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "auto_table", Target: target("bigquery://acme/landing/*"), Routes: true},
	}}

	for i := 0; i < 2; i++ {
		if err := repo.Publish(ctx, manifest(clicks, tables), at); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, pool, "edge"); n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}

	var routes bool
	var tgt *string
	if err := pool.QueryRow(ctx, `SELECT routes, target FROM gateway_destinations
		WHERE gateway='edge' AND stream_path='/v1/tables' AND role='sink'`).Scan(&routes, &tgt); err != nil {
		t.Fatal(err)
	}
	if !routes || tgt == nil || *tgt != "bigquery://acme/landing/*" {
		t.Errorf("routes=%v target=%v", routes, tgt)
	}

	if err := repo.Publish(ctx, manifest(clicks), at); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, "edge"); n != 2 {
		t.Fatalf("after dropping /v1/tables: rows = %d, want 2", n)
	}

	removed, err := repo.Unpublish(ctx, "edge")
	if err != nil || removed != 2 || countRows(t, pool, "edge") != 0 {
		t.Fatalf("unpublish removed %d, err %v", removed, err)
	}
}

// One gateway's publish never touches another's rows.
func TestPublishingOneGatewayLeavesTheOthers(t *testing.T) {
	pool := gatewaysDB(t)
	ctx := context.Background()
	repo := postgres.NewGatewayRepo(pool)
	other := manifest(catalog.ManifestStream{Path: "/v1/x", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "pubsub", Target: target("pubsub://acme/x")}}})
	other.Gateway = "internal"
	if err := repo.Publish(ctx, other, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, manifest(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if countRows(t, pool, "internal") != 1 {
		t.Fatal("publishing edge removed internal's rows")
	}
}
