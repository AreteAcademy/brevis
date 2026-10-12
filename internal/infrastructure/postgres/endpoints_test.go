package postgres_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// endpointsDB truncates api_endpoints AND NOTHING ELSE. loadDB empties runs,
// the queue and the schedules, and the test database is shared by every
// package `go test ./...` runs at once: a scheduler test watching a run was
// failing when these tests emptied the table under it.
func endpointsDB(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the endpoint repository tests (make up)")
	}
	pool, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE api_endpoints`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func api(name string, endpoints ...catalog.Endpoint) catalog.Endpoints {
	return catalog.Endpoints{Version: 1, Kind: "endpoints", Name: name, Connection: "warehouse",
		Dialect: "postgres", Endpoints: endpoints}
}

func endpoint(name, path string, reads ...string) catalog.Endpoint {
	return catalog.Endpoint{Name: name, Path: path, Tenant: "key", Reads: reads,
		Params: []catalog.EndpointParam{{Name: "period", Type: "period"}},
		Limits: catalog.EndpointLimits{Timeout: "10s", MaxRows: 500}}
}

// PUBLISHING REPLACES THE API'S ENDPOINTS: an endpoint removed from the file
// is gone after the next publish, and another API's are untouched.
func TestPublishingAnAPIReplacesItsEndpoints(t *testing.T) {
	pool := endpointsDB(t)
	repo := postgres.NewEndpointRepo(pool)
	ctx := context.Background()
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

	if err := repo.Publish(ctx, api("metrics", endpoint("clicks", "/v1/clicks", "postgres://a/marts/clicks"),
		endpoint("orders", "/v1/orders", "postgres://a/marts/orders")), at); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, api("other", endpoint("x", "/v1/x")), at); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, api("metrics", endpoint("clicks", "/v1/clicks", "postgres://a/marts/clicks")), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	all, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("listed %+v", all)
	}
	for _, e := range all {
		if e.API == "metrics" && e.Name == "orders" {
			t.Error("orders survived a publish that removed it")
		}
	}
	got, found, err := repo.Get(ctx, "metrics", "clicks")
	if err != nil || !found || got.Path != "/v1/clicks" || len(got.Endpoint.Params) != 1 ||
		got.Endpoint.Limits.MaxRows != 500 || !got.PublishedAt.Equal(at.Add(time.Hour)) {
		t.Errorf("clicks: %+v %v %v", got, found, err)
	}
}

// A MODEL'S PAGE ASKS WHICH ENDPOINTS READ IT, by its catalog target.
func TestTheEndpointsOverATargetAreFound(t *testing.T) {
	pool := endpointsDB(t)
	repo := postgres.NewEndpointRepo(pool)
	ctx := context.Background()
	if err := repo.Publish(ctx, api("metrics",
		endpoint("clicks", "/v1/clicks", "postgres://a/marts/clicks", "postgres://a/marts/dim"),
		endpoint("orders", "/v1/orders", "postgres://a/marts/orders")), time.Now()); err != nil {
		t.Fatal(err)
	}
	over, err := repo.Reading(ctx, "postgres://a/marts/dim")
	if err != nil || len(over) != 1 || over[0].Name != "clicks" {
		t.Errorf("%+v %v", over, err)
	}
}

func TestUnpublishingRemovesAnAPI(t *testing.T) {
	pool := endpointsDB(t)
	repo := postgres.NewEndpointRepo(pool)
	ctx := context.Background()
	if err := repo.Publish(ctx, api("metrics", endpoint("clicks", "/v1/clicks")), time.Now()); err != nil {
		t.Fatal(err)
	}
	n, err := repo.Unpublish(ctx, "metrics")
	if err != nil || n != 1 {
		t.Errorf("removed %d, %v", n, err)
	}
}

// TWO ROLLOUTS PUBLISHING THE SAME API TAKE TURNS -- the gateway's lesson,
// where interleaved delete-then-insert put back a row the other had just
// put back and failed on its key.
func TestConcurrentPublishesOfOneAPITakeTurns(t *testing.T) {
	pool := endpointsDB(t)
	repo := postgres.NewEndpointRepo(pool)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = repo.Publish(context.Background(), api("metrics", endpoint("clicks", "/v1/clicks")), time.Now())
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Errorf("a concurrent publish failed: %v", err)
		}
	}
}
