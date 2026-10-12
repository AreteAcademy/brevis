package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/api/cache"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/generation"
)

type memoryKV struct {
	mu   sync.Mutex
	data map[string][]byte
	down bool
}

var errKVDown = errors.New("down")

func (m *memoryKV) Get(_ context.Context, k string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, false, errKVDown
	}
	v, ok := m.data[k]
	return v, ok, nil
}
func (m *memoryKV) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return errKVDown
	}
	m.data[k] = v
	return nil
}
func (m *memoryKV) MGet(_ context.Context, keys ...string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, errKVDown
	}
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = m.data[k]
	}
	return out, nil
}

// counting is a warehouse that counts its reads, optionally slowly.
type counting struct {
	warehouse
	reads atomic.Int32
	delay time.Duration
}

func (c *counting) Read(ctx context.Context, r dialect.Request) (dialect.Result, error) {
	c.reads.Add(1)
	time.Sleep(c.delay)
	return c.warehouse.Read(ctx, r)
}

func cachedServer(t *testing.T, w dialect.Conn, kv *memoryKV, maxRows int) *Server {
	t.Helper()
	e := &Endpoint{Name: "clicks", Path: "/v1/clicks", Tenant: "",
		Params: map[string]Param{"c": {Type: "string", MaxLen: 8}},
		SQL:    "select day, n from marts.clicks_daily where w = {{ tenant }} {% if c %}and c = {{ c }}{% endif %}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	s, err := New(&Config{Name: "x", Env: "local", Dialect: postgres.Dialect{}, MaxRows: maxRows,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Cache:     &CacheConfig{Type: "redis", AddrFrom: "UNUSED", TTL: time.Hour},
		CacheKV:   kv,
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cacheHeader(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := get(h, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
	}
	return rec.Header().Get("X-Brevis-Cache")
}

// THE WHOLE IDEA: a miss, then hits, until the model the SQL reads is
// rebuilt -- and then a miss, with the warehouse asked once per generation.
func TestAnAnswerIsCachedUntilItsModelIsRebuilt(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	s := cachedServer(t, w, kv, 10)

	if got := cacheHeader(t, s, "/v1/clicks?c=x"); got != "miss" {
		t.Errorf("first: %q", got)
	}
	for i := 0; i < 3; i++ {
		if got := cacheHeader(t, s, "/v1/clicks?c=x"); got != "hit" {
			t.Errorf("repeat %d: %q", i, got)
		}
	}
	if n := w.reads.Load(); n != 1 {
		t.Errorf("the warehouse was read %d times for one generation", n)
	}
	// a build rebuilds marts.clicks_daily
	kv.data[generation.Key("marts.clicks_daily")] = []byte("g-2")
	if got := cacheHeader(t, s, "/v1/clicks?c=x"); got != "miss" {
		t.Errorf("after the rebuild: %q", got)
	}
	if n := w.reads.Load(); n != 2 {
		t.Errorf("reads after the rebuild: %d", n)
	}
}

// A HIT SERVES THE SAME BYTES THE MISS DID.
func TestAHitIsTheSameAnswer(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}}}
	s := cachedServer(t, w, &memoryKV{data: map[string][]byte{}}, 10)
	first := get(s, "/v1/clicks").Body.String()
	second := get(s, "/v1/clicks").Body.String()
	if first != second || !strings.Contains(first, `"day":"2026-10-05"`) {
		t.Errorf("miss %s / hit %s", first, second)
	}
}

func TestTwoTenantsNeverShareAnEntry(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	t.Setenv("CACHE_TEST_KEYS", hashOf("acme-key-0123456789abcdef0123456789")+"=acme\n"+
		hashOf("globex-key-0123456789abcdef012345678")+"=globex")
	e := &Endpoint{Name: "clicks", Path: "/v1/clicks", SQL: "select k from t where w = {{ tenant }}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	s, err := New(&Config{Name: "x", Env: "production", Dialect: postgres.Dialect{}, MaxRows: 10,
		Listen:    Listen{Auth: &Auth{Type: "bearer", KeysFrom: "CACHE_TEST_KEYS"}},
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Cache:     &CacheConfig{Type: "redis", AddrFrom: "UNUSED", TTL: time.Hour},
		CacheKV:   kv,
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}
	withKey(s, "/v1/clicks", "acme-key-0123456789abcdef0123456789")
	rec := withKey(s, "/v1/clicks", "globex-key-0123456789abcdef012345678")
	if got := rec.Header().Get("X-Brevis-Cache"); got != "miss" {
		t.Errorf("globex's first request was a %q of acme's entry", got)
	}
	if w.reads.Load() != 2 {
		t.Errorf("%d reads for two tenants", w.reads.Load())
	}
}

// A TRUNCATED ANSWER IS SERVED AND NOT KEPT: it is not the answer, and a
// cache that kept it would keep saying so after the ceiling was raised.
func TestATruncatedAnswerIsNotCached(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}, {2}, {3}}}}
	s := cachedServer(t, w, &memoryKV{data: map[string][]byte{}}, 2)
	cacheHeader(t, s, "/v1/clicks")
	if got := cacheHeader(t, s, "/v1/clicks"); got == "hit" {
		t.Error("a truncated answer was served from the cache")
	}
}

// REDIS DOWN IS A REQUEST ANSWERED FROM THE WAREHOUSE, and counted.
func TestACacheThatIsDownIsBypassedAndCounted(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}}
	kv := &memoryKV{data: map[string][]byte{}, down: true}
	s := cachedServer(t, w, kv, 10)
	if got := cacheHeader(t, s, "/v1/clicks"); got != "bypass" {
		t.Errorf("header %q", got)
	}
	if !strings.Contains(scrape(t, s), `brevis_sql_api_cache_requests_total{endpoint="clicks",result="error"} 1`) {
		t.Errorf("not counted:\n%s", scrape(t, s))
	}
}

// A HUNDRED CONCURRENT MISSES ON ONE KEY ARE ONE QUERY. A rebuild turns
// every answer into a miss at once.
func TestConcurrentMissesAreOneQuery(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}, delay: 50 * time.Millisecond}
	s := cachedServer(t, w, &memoryKV{data: map[string][]byte{}}, 10)
	s.cfg.Limits.MaxInFlight = 1000
	s.inFlight = make(chan struct{}, 1000)
	var wg sync.WaitGroup
	codes := make([]int, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = get(s, "/v1/clicks").Code }(i)
	}
	wg.Wait()
	if n := w.reads.Load(); n != 1 {
		t.Errorf("%d warehouse reads for one key", n)
	}
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("request %d answered %d", i, c)
		}
	}
}

func TestACacheThatCannotBeHonouredIsRefused(t *testing.T) {
	for name, c := range map[string]struct{ cache, says string }{
		"a ttl of zero":    {"cache: {type: redis, addr_from: X, ttl: 0s}", "ttl"},
		"no ttl":           {"cache: {type: redis, addr_from: X}", "ttl"},
		"memcached":        {"cache: {type: memcached, addr_from: X, ttl: 1h}", "memcached"},
		"no addr_from":     {"cache: {type: redis, ttl: 1h}", "addr_from"},
		"a huge max_entry": {"cache: {type: redis, addr_from: X, ttl: 1h, max_entry: 1GiB}", "max_entry"},
	} {
		_, err := fixture(t, strings.Replace(good, "listen:", c.cache+"\nlisten:", 1),
			map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"})
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// THE MODELS AN ENDPOINT READS ARE ITS SQL'S, read once at load from every
// branch: a model read only when a parameter is sent still invalidates.
func TestAnEndpointReadsTheModelsInEveryBranch(t *testing.T) {
	e := &Endpoint{Name: "e", Path: "/v1/e", Params: map[string]Param{"c": {Type: "bool"}},
		SQL: "select * from marts.a where w = {{ tenant }} {% if c %}union all select * from marts.b{% endif %}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.reads, ",") != "marts.a,marts.b" {
		t.Errorf("reads %v", e.reads)
	}
}

func getWith(h http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// A CALLER THAT ACCEPTS BROTLI IS SENT THE STORED BYTES AS THEY ARE: the
// answer was compressed once, when it was kept, and a hit costs a copy.
func TestAHitIsSentCompressedToACallerThatAcceptsBrotli(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"day", "n"}, rows: [][]any{{"2026-10-05", 1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	s := cachedServer(t, w, kv, 10)
	plain := get(s, "/v1/clicks").Body.Bytes()

	rec := getWith(s, "/v1/clicks", map[string]string{"Accept-Encoding": "gzip, br;q=0.9"})
	if rec.Header().Get("Content-Encoding") != "br" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers %v", rec.Header())
	}
	var stored []byte
	for k, v := range kv.data {
		if strings.HasPrefix(k, "brevis:api:") {
			stored = v
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), stored) {
		t.Error("the body is not the stored bytes")
	}
	if got, err := cache.Decompress(rec.Body.Bytes()); err != nil || !bytes.Equal(got, plain) {
		t.Errorf("it decompresses to something else: %v", err)
	}
	// br;q=0 is a refusal of br, not an acceptance
	if rec := getWith(s, "/v1/clicks", map[string]string{"Accept-Encoding": "br;q=0, gzip"}); rec.Header().Get("Content-Encoding") != "" {
		t.Error("br;q=0 was sent br")
	}
}

// AN ETAG IS THE KEY, so it changes exactly when the answer can: a caller
// revalidating gets a 304 until the model is rebuilt.
func TestAnETagRevalidatesUntilTheModelIsRebuilt(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	s := cachedServer(t, w, kv, 10)
	first := get(s, "/v1/clicks")
	tag := first.Header().Get("ETag")
	if tag == "" || first.Header().Get("Cache-Control") != "private, max-age=0" {
		t.Fatalf("headers %v", first.Header())
	}
	rec := getWith(s, "/v1/clicks", map[string]string{"If-None-Match": tag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("revalidation answered %d with %d bytes", rec.Code, rec.Body.Len())
	}
	kv.data[generation.Key("marts.clicks_daily")] = []byte("g-2")
	rec = getWith(s, "/v1/clicks", map[string]string{"If-None-Match": tag})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == tag {
		t.Errorf("after the rebuild: %d, tag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// A SHARED PROXY MUST NOT HOLD A TENANT'S ANSWER, with or without a cache.
func TestEveryAnswerIsPrivate(t *testing.T) {
	rec := get(serverWith(t, &warehouse{cols: []string{"k"}}, clicks(t)), "/v1/clicks?campaign=x")
	if rec.Header().Get("Cache-Control") != "private, max-age=0" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

// A BROTLI HIT ALLOCATES NO COMPRESSOR. The decoder's window is hundreds of
// KiB; a hit that built one per request would be the cache's cost back.
func TestABrotliHitAllocatesNoCodec(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}}
	s := cachedServer(t, w, &memoryKV{data: map[string][]byte{}}, 10)
	get(s, "/v1/clicks")
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			getWith(s, "/v1/clicks", map[string]string{"Accept-Encoding": "br"})
		}
	})
	if per := res.AllocedBytesPerOp(); per > 32<<10 {
		t.Errorf("a brotli hit allocates %d bytes", per)
	}
}
