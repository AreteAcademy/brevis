package api

// Regression tests the ship review found missing: each guards a line that
// could be deleted with every other test still green.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// A CALLER THAT HANGS UP DOES NOT FAIL THE OTHERS waiting on the same key.
// The shared query runs on a context that keeps the request's values and
// drops its cancellation; on the leader's own context, one closed tab would
// be every waiting dashboard's 502.
func TestTheLeaderHangingUpDoesNotFailTheFollowers(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}, delay: 150 * time.Millisecond}
	s := cachedServer(t, w, &memoryKV{data: map[string][]byte{}}, 10)

	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan struct{})
	go func() {
		defer close(leader)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/clicks", nil).WithContext(ctx))
	}()
	for w.reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	follower := make(chan *httptest.ResponseRecorder)
	go func() { follower <- get(s, "/v1/clicks") }()
	time.Sleep(20 * time.Millisecond) // the follower is waiting on the leader's call
	cancel()
	<-leader
	if rec := <-follower; rec.Code != http.StatusOK {
		t.Errorf("the follower answered %d after the leader hung up: %s", rec.Code, rec.Body)
	}
	if n := w.reads.Load(); n != 1 {
		t.Errorf("%d warehouse reads", n)
	}
}

// failing is a warehouse whose error quotes the statement and a value, as
// Postgres's do.
type failing struct{ warehouse }

func (f *failing) Read(context.Context, dialect.Request) (dialect.Result, error) {
	return dialect.Result{}, errors.New(`relation "secret_schema.t" does not exist at select * where c = 'acme-campaign'`)
}

// THE WAREHOUSE'S WORDS NEVER REACH THE CALLER: they quote the SQL the
// author wrote and the value the caller sent.
func TestAWarehouseErrorIsNeverInTheAnswer(t *testing.T) {
	s, err := New(&Config{Name: "x", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 10,
		Open:      func(context.Context) (dialect.Conn, error) { return &failing{}, nil },
		Endpoints: []*Endpoint{clicks(t)}})
	if err != nil {
		t.Fatal(err)
	}
	rec := get(s, "/v1/clicks?campaign=acme")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret_schema") ||
		strings.Contains(rec.Body.String(), "acme") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

// THE API'S CEILING HANDS ITS TOKENS BACK: a token kept per request is an
// API that answers 503 forever after its 32nd.
func TestTheInFlightCeilingFreesItsTokens(t *testing.T) {
	h := limitedServer(t, &warehouse{cols: []string{"k"}}, Limits{MaxInFlight: 2})
	for i := 0; i < 6; i++ {
		if rec := get(h, "/v1/e"); rec.Code != http.StatusOK {
			t.Fatalf("request %d answered %d", i+1, rec.Code)
		}
	}
}

// AN ENDPOINT LOWERS LIMITS AND NEVER RAISES THEM -- max_bytes_billed is
// money -- and the API-wide ceiling is not an endpoint's to set.
func TestAnEndpointMayNotRaiseALimit(t *testing.T) {
	sqlFile := map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"}
	file := strings.Replace(good, "listen:", "limits: {timeout: 5s, max_bytes_billed: 1GiB}\nlisten:", 1)
	for name, c := range map[string]struct{ own, says string }{
		"timeout":          {"{timeout: 10s}", "timeout"},
		"max_bytes_billed": {"{max_bytes_billed: 2GiB}", "max_bytes_billed"},
		"max_in_flight":    {"{max_in_flight: 4}", "max_in_flight"},
	} {
		_, err := fixture(t, file+"    limits: "+c.own+"\n", sqlFile)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// listen.auth IS CHECKED WHEN THE FILE LOADS: a type it does not know, or
// bearer with nowhere to read keys from, is refused before anything starts.
func TestListenAuthIsCheckedAtLoad(t *testing.T) {
	sqlFile := map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"}
	for name, c := range map[string]struct{ auth, says string }{
		"an unknown type": {"{type: basic, keys_from: K}", `"basic"`},
		"no keys_from":    {"{type: bearer}", "keys_from"},
	} {
		_, err := fixture(t, strings.Replace(good, "addr: :8080", "addr: :8080\n  auth: "+c.auth, 1), sqlFile)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h, err := guarded(t, &warehouse{}, hashOf("a-key-long-enough-0123456789abcdef")+"=acme\n"+
		hashOf("b-key-long-enough-0123456789abcdef")+"=acme\n"+hashOf("c-key-long-enough-0123456789abcdef")+"=globex")
	if err != nil {
		t.Fatal(err)
	}
	if keys, tenants := h.Keys(); keys != 3 || tenants != 2 {
		t.Errorf("Keys() = %d, %d", keys, tenants)
	}
}

// A CACHED ENTRY THAT DOES NOT DECOMPRESS IS A MISS, and is replaced: a
// cache is shared state, and one bad entry must not be an endpoint's 500.
func TestACorruptEntryIsAMissAndIsReplaced(t *testing.T) {
	w := &counting{warehouse: warehouse{cols: []string{"k"}, rows: [][]any{{1}}}}
	kv := &memoryKV{data: map[string][]byte{}}
	s := cachedServer(t, w, kv, 10)
	get(s, "/v1/clicks")
	for k := range kv.data {
		if strings.HasPrefix(k, "brevis:api:") {
			kv.data[k] = []byte("not brotli")
		}
	}
	rec := get(s, "/v1/clicks")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Brevis-Cache") != "miss" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("X-Brevis-Cache"))
	}
	if got := cacheHeader(t, s, "/v1/clicks"); got != "hit" {
		t.Errorf("the entry was not replaced: %q", got)
	}
}

// pricedOnly is BigQuery's shape offline: it prices, it reads, and it has
// no read-only guard.
type pricedOnly struct {
	w    *warehouse
	plan dialect.Plan
}

func (p *pricedOnly) Exec(ctx context.Context, s string) error          { return nil }
func (p *pricedOnly) Scalar(ctx context.Context, s string) (any, error) { return nil, nil }
func (p *pricedOnly) Target(string) string                              { return "" }
func (p *pricedOnly) Close(context.Context) error                       { return nil }
func (p *pricedOnly) Read(ctx context.Context, r dialect.Request) (dialect.Result, error) {
	return p.w.Read(ctx, r)
}
func (p *pricedOnly) PlanArgs(context.Context, string, []dialect.Arg) (dialect.Plan, error) {
	return p.plan, nil
}

// A WAREHOUSE THAT PRICES AND HAS NO GUARD READS, within its ceilings --
// the BigQuery path, which otherwise only the live tests reach.
func TestAPricedWarehouseWithNoGuardAnswers(t *testing.T) {
	w := &warehouse{cols: []string{"k"}, rows: [][]any{{1}, {2}}}
	conn := &pricedOnly{w: w, plan: dialect.Plan{Bytes: 10, Kind: "SELECT", Reads: true}}
	rec := get(limitedServer(t, conn, Limits{MaxBytesBilled: 1 << 20}), "/v1/e")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `{"k":2}`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := w.asked[0]; got.Limit != 11 || got.MaxBytes != 1<<20 {
		t.Errorf("read with limit %d and max bytes %d", got.Limit, got.MaxBytes)
	}
}
