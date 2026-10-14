package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// cachedWithAfter is cachedServer's endpoint with an after registered under
// name.
func cachedWithAfter(t *testing.T, w dialect.Conn, kv *memoryKV, name string, after After) *Server {
	t.Helper()
	e := &Endpoint{Name: "clicks", Path: "/v1/clicks",
		Params: map[string]Param{"c": {Type: "string", MaxLen: 8}},
		SQL:    "select day, n from marts.clicks_daily where w = {{ tenant }} {% if c %}and c = {{ c }}{% endif %}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	e.after, e.afterName = after, name
	s, err := New(&Config{Name: "x", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 10,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Cache:     &CacheConfig{Type: "redis", AddrFrom: "UNUSED", TTL: time.Hour},
		CacheKV:   kv,
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// countingAfter counts its calls and returns the row count as the data.
func countingAfter(calls *atomic.Int32) After {
	return func(_ context.Context, r *Result) (any, error) {
		calls.Add(1)
		return map[string]int{"points": len(r.Rows)}, nil
	}
}

// THE CACHE KEEPS WHAT THE HOOK MADE: a hit is the transformed answer, with
// neither the warehouse nor the hook asked again -- and its ETag still
// answers a revalidation with a 304.
func TestAHitServesTheAftersAnswerWithoutCallingIt(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}}}
	var calls atomic.Int32
	s := cachedWithAfter(t, w, &memoryKV{data: map[string][]byte{}}, "points", countingAfter(&calls))

	miss := get(s, "/v1/clicks?c=x")
	hit := get(s, "/v1/clicks?c=x")
	if miss.Header().Get("X-Brevis-Cache") != "miss" || hit.Header().Get("X-Brevis-Cache") != "hit" {
		t.Fatalf("miss %q, then %q", miss.Header().Get("X-Brevis-Cache"), hit.Header().Get("X-Brevis-Cache"))
	}
	if !strings.HasPrefix(hit.Body.String(), `{"data":{"points":1},"truncated":false,`) || hit.Body.String() != miss.Body.String() {
		t.Errorf("miss %s / hit %s", miss.Body, hit.Body)
	}
	if calls.Load() != 1 || w.reads.Load() != 1 {
		t.Errorf("after called %d times, warehouse read %d times", calls.Load(), w.reads.Load())
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/clicks?c=x", nil)
	req.Header.Set("If-None-Match", hit.Header().Get("ETag"))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("a revalidation answered %d", rec.Code)
	}
}

// ONE CALL PER KEY, however many are asking: the misses after a rebuild are
// collapsed, and the hook runs in the one that leads.
func TestConcurrentMissesCallTheAfterOnce(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}},
		delay: 50 * time.Millisecond}
	var calls atomic.Int32
	s := cachedWithAfter(t, w, &memoryKV{data: map[string][]byte{}}, "points", countingAfter(&calls))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := get(s, "/v1/clicks?c=x"); rec.Code != http.StatusOK {
				t.Errorf("status %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("the after ran %d times for one cold key", calls.Load())
	}
}

// A FAILED HOOK KEEPS NOTHING: the next request asks again, rather than
// being answered a 500 out of the cache.
func TestAFailedAfterIsNotCached(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	var calls atomic.Int32
	failing := func(context.Context, *Result) (any, error) {
		calls.Add(1)
		return nil, errors.New("down")
	}
	s := cachedWithAfter(t, w, kv, "points", failing)
	for i := 0; i < 2; i++ {
		if rec := get(s, "/v1/clicks?c=x"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("request %d answered %d", i, rec.Code)
		}
	}
	if len(kv.data) != 0 || calls.Load() != 2 {
		t.Errorf("%d entries kept, after called %d times", len(kv.data), calls.Load())
	}
}

// A DIFFERENT HOOK IS A DIFFERENT ANSWER. The name is in the key, and so is
// the build: a deploy that changes what `points` does under the same name
// must not be answered by what the old one made.
func TestAnotherAfterIsAnotherKey(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	var calls atomic.Int32
	cacheHeader(t, cachedWithAfter(t, w, kv, "points", countingAfter(&calls)), "/v1/clicks?c=x")
	if got := cacheHeader(t, cachedWithAfter(t, w, kv, "series", countingAfter(&calls)), "/v1/clicks?c=x"); got != "miss" {
		t.Errorf("another after's first request was a %q", got)
	}
	if got := cacheHeader(t, cachedServer(t, w, kv, 10), "/v1/clicks?c=x"); got != "miss" {
		t.Errorf("no after, after an after, was a %q", got)
	}
}

func TestTheBuildIsInTheShape(t *testing.T) {
	if shapeOf("points", "v1") == shapeOf("points", "v2") || shapeOf("points", "v1") == shapeOf("series", "v1") {
		t.Error("the shape does not change with the build or the name")
	}
	if shapeOf("", "v1") != "" {
		t.Error("an endpoint with no after has a shape")
	}
}
