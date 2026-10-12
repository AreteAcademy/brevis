package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// slow blocks every Read until released or until its context ends.
type slow struct {
	warehouse
	release chan struct{}
	entered chan struct{}
}

func (s *slow) Read(ctx context.Context, r dialect.Request) (dialect.Result, error) {
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	select {
	case <-s.release:
		return dialect.Result{Columns: []string{"k"}}, nil
	case <-ctx.Done():
		return dialect.Result{}, ctx.Err()
	}
}

func limitedServer(t *testing.T, conn dialect.Conn, lim Limits) http.Handler {
	t.Helper()
	e := &Endpoint{Name: "e", Path: "/v1/e", SQL: "select k from t where w = {{ tenant }}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	h, err := New(&Config{Name: "x", Env: "local", Dialect: postgres.Dialect{},
		Open:      func(context.Context) (dialect.Conn, error) { return conn, nil },
		MaxRows:   10,
		Limits:    lim,
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// THE CLOCK CANCELS THE QUERY, and says it did: a request that waits on a
// warehouse forever holds a connection and a slot forever.
func TestATimeoutCancelsTheQueryAnd504s(t *testing.T) {
	s := &slow{release: make(chan struct{})}
	h := limitedServer(t, s, Limits{Timeout: 20 * time.Millisecond})
	rec := get(h, "/v1/e")
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "20ms") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

// FOUR IN FLIGHT PER TENANT, and the fifth is told when to come back. Per
// TENANT, so one customer's dashboard refreshing in a loop cannot take the
// slots another customer's request needs.
func TestAFifthRequestInFlightFromOneTenantIs429(t *testing.T) {
	s := &slow{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	h := limitedServer(t, s, Limits{Timeout: 5 * time.Second})
	var wg sync.WaitGroup
	for i := 0; i < perTenantInFlight; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); get(h, "/v1/e") }()
	}
	for i := 0; i < perTenantInFlight; i++ {
		<-s.entered
	}
	rec := get(h, "/v1/e")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("the fifth answered %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	close(s.release)
	wg.Wait()
	if rec := get(h, "/v1/e"); rec.Code != http.StatusOK {
		t.Errorf("after the four finished: %d", rec.Code)
	}
}

// guarded records that the read ran inside the read-only guard.
type guardedConn struct {
	warehouse
	inside bool
	read   bool
}

func (g *guardedConn) ReadOnly(ctx context.Context, fn func(context.Context) error) error {
	g.inside = true
	defer func() { g.inside = false }()
	return fn(ctx)
}

func (g *guardedConn) Read(ctx context.Context, r dialect.Request) (dialect.Result, error) {
	g.read = g.inside
	return dialect.Result{Columns: []string{"k"}}, nil
}

// POSTGRES READS INSIDE ITS READ-ONLY GUARD, so a template that turned out
// to write is refused by the warehouse and not by a parser's opinion.
func TestTheReadRunsInsideTheReadOnlyGuard(t *testing.T) {
	g := &guardedConn{}
	if rec := get(limitedServer(t, g, Limits{}), "/v1/e"); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !g.read {
		t.Error("the read ran outside the guard")
	}
}

// planned is a warehouse that prices before it runs.
type planned struct {
	warehouse
	plan dialect.Plan
}

func (p *planned) PlanArgs(context.Context, string, []dialect.Arg) (dialect.Plan, error) {
	return p.plan, nil
}

// A WAREHOUSE THAT PRICES IS ASKED FIRST: its verdict on the kind of
// statement is what makes it read-only, and its estimate is what refuses an
// expensive query before the money is spent. The ceiling also travels on
// the request, because a priced query can grow before it runs.
func TestAPricedWarehouseIsAskedKindAndCostFirst(t *testing.T) {
	gib := ByteSize(1 << 30)
	cases := []struct {
		plan dialect.Plan
		code int
		says string
	}{
		{dialect.Plan{Bytes: 1 << 20, Kind: "SELECT", Reads: true}, http.StatusOK, ""},
		{dialect.Plan{Bytes: 2 << 30, Kind: "SELECT", Reads: true}, http.StatusUnprocessableEntity, "1 GiB"},
		{dialect.Plan{Bytes: 1, Kind: "UPDATE", Reads: false}, http.StatusInternalServerError, "UPDATE"},
	}
	for _, c := range cases {
		p := &planned{warehouse: warehouse{cols: []string{"k"}}, plan: c.plan}
		rec := get(limitedServer(t, p, Limits{MaxBytesBilled: gib}), "/v1/e")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%+v: %d %s", c.plan, rec.Code, rec.Body)
		}
		if c.code == http.StatusOK && (len(p.asked) != 1 || p.asked[0].MaxBytes != int64(gib)) {
			t.Errorf("the read was %+v", p.asked)
		}
		if c.code != http.StatusOK && len(p.asked) != 0 {
			t.Errorf("%+v: read anyway", c.plan)
		}
	}
}

// A WAREHOUSE THAT CAN BE MADE NEITHER READ-ONLY NOR PRICED IS REFUSED, at
// the request and with no read: fail closed.
func TestAWarehouseThatCanBeMadeNeitherReadOnlyNorPricedIsRefused(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	rec := get(limitedServer(t, &plainConn{w}, Limits{}), "/v1/e")
	if rec.Code != http.StatusInternalServerError || len(w.asked) != 0 {
		t.Errorf("%d %s, asked %d", rec.Code, rec.Body, len(w.asked))
	}
}

// plainConn is a Reader with no guard and no planner: it delegates rather
// than embeds, so it inherits neither.
type plainConn struct{ w *warehouse }

func (p *plainConn) Exec(ctx context.Context, s string) error          { return p.w.Exec(ctx, s) }
func (p *plainConn) Scalar(ctx context.Context, s string) (any, error) { return p.w.Scalar(ctx, s) }
func (p *plainConn) Target(r string) string                            { return p.w.Target(r) }
func (p *plainConn) Close(ctx context.Context) error                   { return p.w.Close(ctx) }
func (p *plainConn) Read(ctx context.Context, r dialect.Request) (dialect.Result, error) {
	return p.w.Read(ctx, r)
}

func TestLimitsInTheFile(t *testing.T) {
	cfg, err := fixture(t, strings.Replace(good, "listen:", "limits: {timeout: 3s, max_rows: 500, max_bytes_billed: 2GiB}\nlisten:", 1)+
		"    limits: {max_rows: 100}\n",
		map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.Timeout != 3*time.Second || cfg.MaxRows != 500 || cfg.Limits.MaxBytesBilled != 2<<30 {
		t.Errorf("file limits %+v, max rows %d", cfg.Limits, cfg.MaxRows)
	}
	if cfg.Endpoints[1].MaxRows != 100 {
		t.Errorf("the endpoint's ceiling is %d", cfg.Endpoints[1].MaxRows)
	}
	for name, c := range map[string]struct{ file, says string }{
		"an endpoint raising rows": {strings.Replace(good, "listen:", "limits: {max_rows: 50}\nlisten:", 1) + "    limits: {max_rows: 100}\n", "lower"},
		"a size that is not one":   {strings.Replace(good, "listen:", "limits: {max_bytes_billed: lots}\nlisten:", 1), "lots"},
		"a negative timeout":       {strings.Replace(good, "listen:", "limits: {timeout: -1s}\nlisten:", 1), "timeout"},
		"rows over the hard cap":   {strings.Replace(good, "listen:", "limits: {max_rows: 1000000}\nlisten:", 1), "max_rows"},
	} {
		_, err := fixture(t, c.file, map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"})
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A CEILING FOR THE WHOLE API, not only per tenant. Found in review: each
// request opens its own warehouse connection, so twenty-five tenants at four
// each is a hundred connections -- Postgres's default max_connections -- and
// every tenant, and whatever else uses that database, is refused.
func TestTheWholeAPIHasACeilingInFlight(t *testing.T) {
	s := &slow{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	h := limitedServer(t, s, Limits{Timeout: 5 * time.Second, MaxInFlight: 2})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); get(h, "/v1/e") }()
	}
	<-s.entered
	<-s.entered
	rec := get(h, "/v1/e")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("the third answered %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	close(s.release)
	wg.Wait()
}

func TestAMaxInFlightBelowOnePerTenantIsRefused(t *testing.T) {
	_, err := fixture(t, strings.Replace(good, "listen:", "limits: {max_in_flight: -1}\nlisten:", 1),
		map[string]string{"endpoints/clicks.sql": "select {{ tenant }} {{ campaign }}"})
	if err == nil || !strings.Contains(err.Error(), "max_in_flight") {
		t.Errorf("got %v", err)
	}
}

// A SIZE THAT WOULD OVERFLOW IS REFUSED, not wrapped. Found in review:
// `16777216TiB` wrapped to zero, which then read as "take the default".
func TestASizeThatOverflowsIsRefused(t *testing.T) {
	var b ByteSize
	for _, raw := range []string{"16777216TiB", "9223372036854775807KiB", "-1GiB"} {
		if err := b.UnmarshalYAML(&yaml.Node{Kind: yaml.ScalarNode, Value: raw}); err == nil {
			t.Errorf("%s read as %d", raw, b)
		}
	}
}
