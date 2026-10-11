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
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE gateway_destinations, gateway_streams, gateway_config_history, writer_configs CASCADE`); err != nil {
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

func count(t *testing.T, pool *postgres.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func configured(every string, notes map[string]string) catalog.ManifestStream {
	return catalog.ManifestStream{Name: "tables", Path: "/v1/tables",
		Fields: []catalog.ManifestField{
			{Path: "buffer.flush.every", Value: every},
			{Path: "buffer.workers", Value: float64(4), Default: true},
			{Path: "sink.into.write", Value: "merge"},
		},
		Notes: notes,
		Destinations: []catalog.ManifestDestination{
			{Role: "sink", Kind: "auto_table", Target: target("bigquery://acme/landing/*"), Routes: true},
		}}
}

func v2(streams ...catalog.ManifestStream) catalog.Manifest {
	m := manifest(streams...)
	m.Version = 2
	return m
}

// A configuration is stored once, whatever the number of publishes, and the
// history gains a line only when the configuration CHANGED.
func TestPublishingAConfigurationKeepsOneCopyAndAHistoryOfChanges(t *testing.T) {
	pool := gatewaysDB(t)
	ctx := context.Background()
	repo := postgres.NewGatewayRepo(pool)
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	history := `SELECT count(*) FROM gateway_config_history WHERE gateway = 'edge' AND stream_path = '/v1/tables'`

	for i := range 2 {
		if err := repo.Publish(ctx, v2(configured("1m", nil)), at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM writer_configs`); n != 1 {
		t.Fatalf("writer_configs = %d, want 1", n)
	}
	if n := count(t, pool, history); n != 1 {
		t.Fatalf("history after two equal publishes = %d, want 1", n)
	}

	if err := repo.Publish(ctx, v2(configured("2m", nil)), at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, history); n != 2 {
		t.Fatalf("history after a change = %d, want 2", n)
	}

	var hash string
	if err := pool.QueryRow(ctx, `SELECT config_hash FROM gateway_streams
		WHERE gateway = 'edge' AND stream_path = '/v1/tables'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	want, _ := configured("2m", nil).ConfigHash()
	if hash != want {
		t.Fatalf("the stream points at %s, want the latest configuration %s", hash, want)
	}
}

// A comment reworded is the same version, and the page shows the newest words.
func TestRewordedNotesReplaceTheOldOnesWithoutAVersion(t *testing.T) {
	pool := gatewaysDB(t)
	ctx := context.Background()
	repo := postgres.NewGatewayRepo(pool)
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	if err := repo.Publish(ctx, v2(configured("1m", map[string]string{"": "first words"})), at); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, v2(configured("1m", map[string]string{"": "second words"})), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM gateway_config_history`); n != 1 {
		t.Fatalf("history = %d, want 1", n)
	}
	var note string
	if err := pool.QueryRow(ctx, `SELECT notes->>'' FROM gateway_streams WHERE gateway = 'edge'`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != "second words" {
		t.Fatalf("note = %q", note)
	}
}

// A version-1 manifest carries no configuration: the stream is listed, with
// nothing to point at and nothing in the history.
func TestAVersionOnePublishHasNoConfiguration(t *testing.T) {
	pool := gatewaysDB(t)
	ctx := context.Background()
	repo := postgres.NewGatewayRepo(pool)
	old := catalog.ManifestStream{Path: "/v1/clicks", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "pubsub", Target: target("pubsub://acme/clicks")}}}

	if err := repo.Publish(ctx, manifest(old), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM gateway_streams WHERE config_hash IS NULL`); n != 1 {
		t.Fatalf("streams with no configuration = %d, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM gateway_config_history`); n != 0 {
		t.Fatalf("history = %d, want 0", n)
	}
}

// Unpublishing removes the gateway from /data; what it was configured with
// over time stays -- that is the record an audit asks for.
func TestUnpublishingKeepsTheHistory(t *testing.T) {
	pool := gatewaysDB(t)
	ctx := context.Background()
	repo := postgres.NewGatewayRepo(pool)
	if err := repo.Publish(ctx, v2(configured("1m", nil)), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Unpublish(ctx, "edge"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM gateway_streams`); n != 0 {
		t.Fatalf("streams after unpublish = %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM gateway_config_history`); n != 1 {
		t.Fatalf("history after unpublish = %d, want 1", n)
	}
}
