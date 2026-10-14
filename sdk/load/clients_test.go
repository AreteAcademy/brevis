package load

import (
	"context"
	"errors"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/option"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// emulated points the SDK at the fake BigQuery for one test, with an empty
// client cache, so no test is handed a client another one made.
func emulated(t *testing.T) *fakeBigQuery {
	t.Helper()
	f := newFakeBigQuery()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv(EnvEmulator, srv.URL)
	resetClients(t)
	return f
}

func cfgFor(project string) *core.LoadConfig {
	return &core.LoadConfig{ProjectID: project, Dataset: "d", Table: "t", Format: "ndjson"}
}

func settledGoroutines() int {
	runtime.GC()
	time.Sleep(300 * time.Millisecond)
	return runtime.NumGoroutine()
}

// #87, measured before writing it: 50 New + Job left 150 goroutines behind,
// three per call, and closing both clients changed nothing -- every New built
// an HTTP transport of its own, and its idle connections outlive Close. The
// gateway pays that on every batch it writes, not only when it reconciles.
func TestRepeatedLoadsDoNotGrowTheGoroutines(t *testing.T) {
	emulated(t)
	ctx := context.Background()

	ask := func() {
		l, err := New(ctx, cfgFor("p"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Job(ctx, "brevis_never_g1", "US"); err != nil {
			t.Fatal(err)
		}
	}
	ask()
	after1 := settledGoroutines()
	for i := 0; i < 49; i++ {
		ask()
	}
	if after50 := settledGoroutines(); after50 > after1+5 {
		t.Errorf("goroutines went from %d after one load to %d after fifty", after1, after50)
	}
}

func TestOneProjectIsOneClient(t *testing.T) {
	emulated(t)
	ctx := context.Background()
	a, _ := New(ctx, cfgFor("p"))
	b, _ := New(ctx, cfgFor("p"))
	c, _ := New(ctx, cfgFor("q"))
	if a.bq != b.bq {
		t.Error("two loaders of one project built two BigQuery clients")
	}
	if a.bq == c.bq {
		t.Error("two projects share one BigQuery client")
	}
}

// Asking about a job and loading inline never touch GCS, and the storage
// client they used to build was a transport of its own on every call.
func TestAJobAndAnInlineLoadBuildNoStorageClient(t *testing.T) {
	emulated(t)
	ctx := context.Background()
	l, err := New(ctx, cfgFor("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Job(ctx, "brevis_never_g1", "US"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := l.loadInline(ctx, l.bq.Dataset("d").Table("t"), []byte(`{"id":"first"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	if n := gcsMade.Load(); n != 0 {
		t.Errorf("%d storage client(s) built for a job lookup and an inline load", n)
	}
}

// Staging builds the storage client once, and the next load that stages gets
// the same one.
func TestStagingSharesOneStorageClient(t *testing.T) {
	emulated(t)
	ctx := context.Background()
	a, err := sharedStorage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sharedStorage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || gcsMade.Load() != 1 {
		t.Errorf("two stagings built %d storage clients", gcsMade.Load())
	}
}

// A client that could not be built is not remembered: credentials appear, a
// network comes back, and the next call must try again rather than replay
// the failure for the rest of the process.
func TestAFailedClientIsNotCached(t *testing.T) {
	emulated(t)
	real := newBigQuery
	t.Cleanup(func() { newBigQuery = real })
	newBigQuery = func(context.Context, string, ...option.ClientOption) (*bigquery.Client, error) {
		return nil, errors.New("no credentials yet")
	}
	if _, err := New(context.Background(), cfgFor("p")); err == nil {
		t.Fatal("the failure did not surface")
	}
	newBigQuery = real
	if _, err := New(context.Background(), cfgFor("p")); err != nil {
		t.Errorf("after a failure, a working build still fails: %v", err)
	}
}

// The client outlives the call that built it. Built on the caller's context, a
// gateway's 60-second send window would end the context a cached client's
// token refresh later runs on.
func TestAClientOutlivesTheContextThatBuiltIt(t *testing.T) {
	emulated(t)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := New(ctx, cfgFor("p")); err != nil {
		t.Fatal(err)
	}
	cancel()
	l, err := New(context.Background(), cfgFor("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Job(context.Background(), "brevis_never_g1", "US"); err != nil {
		t.Errorf("the cached client broke when its first caller's context ended: %v", err)
	}
}

func TestLoadersShareClientsAcrossGoroutines(t *testing.T) {
	emulated(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := New(context.Background(), cfgFor("p"))
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := l.Job(context.Background(), "brevis_never_g1", "US"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := bqMade.Load(); n != 1 {
		t.Errorf("20 concurrent loaders of one project built %d BigQuery clients", n)
	}
}
