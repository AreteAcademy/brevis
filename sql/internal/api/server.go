package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/api/cache"
	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/generation"
)

// Server answers the endpoints of one file.
type Server struct {
	cfg    *Config
	binder dialect.Binder
	mux    *http.ServeMux
	keys   *keyring
	slots  slots
	met    *metrics
	// cache and flight are the answers kept, and the misses collapsed; nil
	// cache is none declared.
	cache  *cache.Store
	flight cache.Group[outcome]

	// inFlight is the whole API's ceiling, one token per request waiting on
	// the warehouse.
	inFlight chan struct{}
}

// New builds the handler, or refuses a configuration it cannot serve.
func New(cfg *Config) (*Server, error) {
	b, binds := cfg.Dialect.(dialect.Binder)
	if !binds {
		return nil, fmt.Errorf("the %s dialect cannot bind arguments, and this API sends no value any other way",
			cfg.Dialect.Name())
	}
	var keys *keyring
	if cfg.Listen.Auth == nil {
		if cfg.Env != EnvLocal {
			return nil, fmt.Errorf("BREVIS_ENV=%s requires `listen.auth`: an API with no "+
				"authentication hands every tenant's rows to whoever finds the address. "+
				"Declare `{type: bearer, keys_from: <env var>}`, or run with BREVIS_ENV=%s "+
				"if this is a laptop", cfg.Env, EnvLocal)
		}
	} else {
		var err error
		if keys, err = loadKeys(cfg.Listen.Auth.KeysFrom); err != nil {
			return nil, err
		}
	}
	ceiling := cfg.Limits.MaxInFlight
	if ceiling == 0 {
		ceiling = defaultMaxInFlight
	}
	s := &Server{cfg: cfg, binder: b, mux: http.NewServeMux(), keys: keys, met: newMetrics(),
		inFlight: make(chan struct{}, ceiling)}
	if cfg.Cache != nil {
		kv := cfg.CacheKV
		if kv == nil {
			addr := os.Getenv(cfg.Cache.AddrFrom)
			if addr == "" {
				return nil, fmt.Errorf("`cache.addr_from` names %s and it is empty", cfg.Cache.AddrFrom)
			}
			c, err := generation.Open(addr)
			if err != nil {
				return nil, fmt.Errorf("cache: %v", err)
			}
			kv = c
		}
		maxEntry := cfg.Cache.MaxEntry
		if maxEntry == 0 {
			maxEntry = defaultMaxEntry
		}
		s.cache = cache.New(kv, cfg.Cache.TTL, int(maxEntry))
	}
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	endpoints := http.NewServeMux()
	for _, e := range cfg.Endpoints {
		endpoints.Handle(e.Path, s.endpoint(e))
	}
	s.mux.Handle("/", guard(keys, func(w http.ResponseWriter, r *http.Request, tenant string) {
		endpoints.ServeHTTP(w, r.WithContext(withTenant(r.Context(), tenant)))
	}, func(started time.Time) {
		s.finish(&record{Endpoint: noEndpoint, Status: http.StatusUnauthorized}, started, 0)
	}))
	return s, nil
}

// Keys is how many keys and tenants the API accepts; zero and zero means it
// is open, which only BREVIS_ENV=local allows.
func (s *Server) Keys() (keys, tenants int) {
	if s.keys == nil {
		return 0, 0
	}
	return s.keys.count()
}

type tenantKey struct{}

func withTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// tenantOf is the tenant guard put on the request. A request that reached
// an endpoint without passing guard has none, and is refused rather than
// served as nobody in particular.
func tenantOf(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tenantKey{}).(string)
	return t, ok && t != ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) endpoint(e *Endpoint) http.Handler {
	ceiling := e.MaxRows
	if ceiling == 0 {
		ceiling = s.cfg.MaxRows
	}
	timeout := e.timeout
	if timeout == 0 {
		timeout = s.cfg.Limits.Timeout
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxBytes := e.maxBytes
	if maxBytes == 0 {
		maxBytes = s.cfg.Limits.MaxBytesBilled
	}
	maxResponse := e.maxResponse
	if maxResponse == 0 {
		maxResponse = s.cfg.Limits.MaxResponseBytes
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// ONE LINE AND ONE COUNT PER REQUEST, whatever it ended as: the
		// status is read off the writer, so a refusal added next year is
		// counted without anybody remembering to.
		started, line, warehouse := time.Now(), &record{Endpoint: e.Name}, time.Duration(0)
		w := &statusWriter{ResponseWriter: rw}
		defer func() {
			line.Status = w.status
			s.finish(line, started, warehouse)
		}()

		// READ ONLY, and the method says so: a body is never read, so a POST
		// is not a different way to ask but a request this does not take.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			refuse(w, http.StatusMethodNotAllowed, "this endpoint answers GET and HEAD")
			return
		}
		tenant, known := tenantOf(r.Context())
		if !known {
			refuse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		line.Tenant = tenant
		line.Params = e.sent(r)
		if e.before != nil {
			// THE CALLER'S QUERY, AS THE HOOK LEAVES IT, is what is read
			// from here on -- through every check a caller's query meets.
			// The audit line above already holds what the caller sent.
			//
			// IN ONE OF THE TENANT'S SLOTS, which the hook keeps until it
			// returns: a before runs ahead of the warehouse's ceilings, and
			// without this a key could pile up hooks without limit.
			release, free := s.slots.take(tenant)
			if !free {
				w.Header().Set("Retry-After", "1")
				refuse(w, http.StatusTooManyRequests, fmt.Sprintf(
					"%d requests from this key's tenant are already in flight", perTenantInFlight))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			rewritten, status, why := runBefore(ctx, s.met, e, tenant, r, release)
			cancel()
			if status != 0 {
				line.Hook = "before"
				switch status {
				case http.StatusBadRequest:
				case http.StatusGatewayTimeout:
					line.Reason = why
					why = fmt.Sprintf("this endpoint's before hook did not answer within %s", timeout)
				default:
					line.Reason = why
					why = "this endpoint's before hook failed; its author has the reason in the API's log"
				}
				refuse(w, status, why)
				return
			}
			r = rewritten
		}
		vals, err := e.values(r)
		if err != nil {
			refuse(w, http.StatusBadRequest, err.Error())
			return
		}
		vals[tenantName] = render.Bind(tenant)
		query, args, err := e.Template.Render(vals, s.binder)
		if err != nil {
			// THE AUTHOR'S, NOT THE CALLER'S. Every value was valid by now; a
			// template that still cannot render -- a name used outside an
			// `{% if %}` with no default, a branch that leaves a placeholder
			// in a string -- is a broken endpoint, and telling the caller to
			// edit a template they cannot see helps nobody. The reason goes to
			// the audit line, for whoever owns the file.
			line.Reason = err.Error()
			refuse(w, http.StatusInternalServerError, "this endpoint's SQL cannot be rendered for this request; "+
				"its author has the reason in the API's log")
			return
		}
		line.ArgsHash = argsHash(query, args)
		// A SHARED PROXY MUST NOT HOLD A TENANT'S ANSWER: private, and
		// revalidated every time -- against the ETag, when there is one.
		w.Header().Set("Cache-Control", "private, max-age=0")

		var shape *shaping
		if e.after != nil {
			shape = &shaping{after: e.after, params: plainParams(vals)}
		}
		ask := func(parent context.Context) outcome {
			return s.answer(parent, e.Name, tenant, query, args, ceiling, timeout, maxBytes, maxResponse, shape)
		}
		var o outcome
		if s.cache == nil {
			o = ask(r.Context())
		} else {
			o = s.cached(r, w, line, e, tenant, query, args, shape, ask)
			if o.served {
				return
			}
		}
		warehouse = o.warehouse
		line.Rows, line.Bytes = o.rows, o.scanned
		if o.hook != "" {
			line.Hook, line.Reason = o.hook, o.reason
		}
		if o.status != 0 {
			if o.retry {
				w.Header().Set("Retry-After", "1")
			}
			refuse(w, o.status, o.why)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(o.body)
	})
}

// acceptsBrotli reads Accept-Encoding for `br` with a weight above zero --
// `br;q=0` is a refusal, not an acceptance.
func acceptsBrotli(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "br") {
			continue
		}
		q := strings.TrimSpace(params)
		if v, found := strings.CutPrefix(q, "q="); found {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f == 0 {
				return false
			}
		}
		return true
	}
	return false
}

// matches reads If-None-Match: a list of tags, weak or strong, or `*`.
func matches(header, tag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == tag || t == "*" {
			return true
		}
	}
	return false
}

// outcome is one answer from the warehouse, or why there is none.
type outcome struct {
	body      []byte
	rows      int
	scanned   int64
	truncated bool
	warehouse time.Duration

	status int
	why    string
	retry  bool

	// hook and reason are the audit line's, when a hook decided the status.
	hook   string
	reason string

	// served says the cache already wrote the response.
	served bool
}

// answer asks the warehouse, within every ceiling -- the whole API's, the
// tenant's, the clock's, the rows' and the bytes'.
func (s *Server) answer(parent context.Context, endpoint, tenant, query string, args []dialect.Arg,
	ceiling int, timeout time.Duration, maxBytes, maxResponse ByteSize, shape *shaping) outcome {
	// THE WHOLE API FIRST, then the tenant: a 503 says the service is full,
	// a 429 says this tenant is, and the caller is owed the right one.
	select {
	case s.inFlight <- struct{}{}:
		defer func() { <-s.inFlight }()
	default:
		return outcome{status: http.StatusServiceUnavailable, retry: true,
			why: "the API is at its ceiling of requests waiting on the warehouse"}
	}
	release, free := s.slots.take(tenant)
	if !free {
		return outcome{status: http.StatusTooManyRequests, retry: true, why: fmt.Sprintf(
			"%d requests from this key's tenant are already waiting on the warehouse", perTenantInFlight)}
	}
	// THE SLOT IS HANDED TO THE AFTER when there is one, which gives it back
	// when it returns -- after this has, if it hangs.
	held := release
	defer func() {
		if held != nil {
			held()
		}
	}()

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	asked := time.Now()
	res, status, why := s.read(ctx, query, args, ceiling, maxBytes)
	took := time.Since(asked)
	if status != 0 {
		if ctx.Err() == context.DeadlineExceeded {
			status, why = http.StatusGatewayTimeout,
				fmt.Sprintf("the query took longer than %s and was cancelled", timeout)
		}
		return outcome{status: status, why: why, warehouse: took}
	}
	truncated := res.Truncated
	if len(res.Rows) > ceiling {
		res.Rows, truncated = res.Rows[:ceiling], true
	}
	if shape != nil {
		// INSIDE THE TIMEOUT, ON THE SAME CONTEXT, and bounded by it whether
		// the hook reads it or not.
		r := &Result{Endpoint: endpoint, Tenant: tenant, Params: shape.params,
			Columns: res.Columns, Rows: res.Rows, Truncated: truncated}
		shaped := time.Now()
		slot := held
		held = nil
		body, err := shapeWith(ctx, shape.after, r, maxResponse, slot)
		s.met.hook(endpoint, "after", outcomeOf(err), time.Since(shaped))
		if err != nil {
			o := outcome{status: http.StatusInternalServerError, warehouse: took, hook: "after", reason: err.Error(),
				why: "this endpoint's after hook failed; its author has the reason in the API's log"}
			var late hookTimeout
			if errors.As(err, &late) {
				o.status, o.why = http.StatusGatewayTimeout,
					fmt.Sprintf("this endpoint's after hook did not answer within %s", timeout)
			}
			return o
		}
		return outcome{body: body, rows: len(res.Rows), scanned: res.Scanned, truncated: truncated, warehouse: took}
	}
	body, kept, err := encode(endpoint, res, truncated, maxResponse)
	if err != nil {
		return outcome{status: http.StatusInternalServerError, why: "the answer could not be written as JSON", warehouse: took}
	}
	return outcome{body: body, rows: kept, scanned: res.Scanned, truncated: truncated || kept < len(res.Rows), warehouse: took}
}

// cacheTimeout bounds every round trip to the cache. A cache slower than
// this is one the request is better off without.
const cacheTimeout = 500 * time.Millisecond

// cached answers from the cache when it can, and otherwise asks once per key
// however many requests are asking -- the misses after a rebuild, collapsed.
//
// A CACHE NEVER FAILS A REQUEST: an error anywhere here is a request answered
// from the warehouse, marked `bypass` and counted as an error.
func (s *Server) cached(r *http.Request, w http.ResponseWriter, line *record, e *Endpoint,
	tenant, query string, args []dialect.Arg, shape *shaping, ask func(context.Context) outcome) outcome {
	rctx := r.Context()
	w.Header().Set("Vary", "Accept-Encoding")
	cctx, cancel := context.WithTimeout(rctx, cacheTimeout)
	defer cancel()
	gens, err := s.cache.Generations(cctx, e.reads)
	var key string
	if err == nil {
		key = cache.Key(s.cfg.Name, s.cfg.ConnectionID, e.Name, tenant, query, args, gens, keyShape(e, shape))
		// THE ETAG IS THE KEY'S HASH, so it changes exactly when the answer
		// can: the same request over the same generations.
		tag := `"` + key[strings.LastIndexByte(key, ':')+1:][:32] + `"`
		w.Header().Set("ETag", tag)
		var stored []byte
		var found bool
		if stored, found, err = s.cache.Get(cctx, key); err == nil && found {
			line.Cache = "hit"
			w.Header().Set("X-Brevis-Cache", "hit")
			if matches(r.Header.Get("If-None-Match"), tag) {
				w.WriteHeader(http.StatusNotModified)
				return outcome{served: true}
			}
			w.Header().Set("Content-Type", "application/json")
			// THE STORED BYTES, AS THEY ARE, to a caller that takes Brotli:
			// the answer was compressed once, when it was kept.
			if acceptsBrotli(r.Header.Get("Accept-Encoding")) {
				w.Header().Set("Content-Encoding", "br")
				_, _ = w.Write(stored)
				return outcome{served: true}
			}
			if body, derr := cache.Decompress(stored); derr == nil {
				_, _ = w.Write(body)
				return outcome{served: true}
			}
			line.Cache = "miss"
			// AN ENTRY THAT DOES NOT DECOMPRESS IS A MISS, and is about to
			// be overwritten by a good one.
		}
	}
	if err != nil {
		line.Cache = "error"
		w.Header().Del("ETag")
		w.Header().Set("X-Brevis-Cache", "bypass")
		return ask(rctx)
	}

	line.Cache = "miss"
	w.Header().Set("X-Brevis-Cache", "miss")
	// THE LEADER'S CONTEXT IS NOT THE FOLLOWERS'. A caller that hangs up
	// must not fail everybody waiting on the same key, so the shared query
	// runs on a context that keeps the request's values and drops its
	// cancellation -- the timeout inside `answer` still bounds it.
	o, _ := s.flight.Do(key, func() (outcome, error) {
		o := ask(context.WithoutCancel(rctx))
		if o.status == 0 && !o.truncated {
			pctx, cancel := context.WithTimeout(context.WithoutCancel(rctx), cacheTimeout)
			defer cancel()
			if _, err := s.cache.Put(pctx, key, o.body); err != nil {
				line.Cache = "error"
			}
		}
		return o, nil
	})
	return o
}

// read runs one rendered statement, read-only, within its ceilings. A
// non-zero status is a refusal, with what to tell the caller.
//
// READ-ONLY BY THE WAREHOUSE, EACH IN ITS OWN WAY, and fail closed. A
// warehouse that prices (BigQuery) is asked first: its verdict on the kind
// of statement is what makes it read-only there, and its estimate refuses an
// expensive query before the money is spent. One with a read-only guard
// (Postgres) reads inside it. One that offers neither is refused, because
// the only thing left between a template and a write would be the author.
func (s *Server) read(ctx context.Context, query string, args []dialect.Arg, ceiling int, maxBytes ByteSize) (dialect.Result, int, string) {
	conn, err := s.cfg.Open(ctx)
	if err != nil {
		return dialect.Result{}, http.StatusBadGateway, "the warehouse could not be reached"
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	reader, reads := conn.(dialect.Reader)
	if !reads {
		return dialect.Result{}, http.StatusBadGateway, "the warehouse connection cannot return rows"
	}
	planner, prices := conn.(dialect.ArgPlanner)
	guard, guards := conn.(dialect.ReadOnlyGuard)
	if !prices && !guards {
		return dialect.Result{}, http.StatusInternalServerError,
			"the warehouse can be made neither read-only nor priced, and this API reads only from one that can"
	}
	if prices {
		plan, err := planner.PlanArgs(ctx, query, args)
		if err != nil {
			return dialect.Result{}, http.StatusBadGateway, "the warehouse refused to price the query"
		}
		if !plan.Reads {
			// THE KIND IS THE WAREHOUSE'S OWN WORD and safe to quote; the
			// statement is not.
			return dialect.Result{}, http.StatusInternalServerError,
				fmt.Sprintf("the endpoint's SQL is not a read: the warehouse calls it %s", plan.Kind)
		}
		if maxBytes > 0 && ByteSize(plan.Bytes) > maxBytes {
			return dialect.Result{}, http.StatusUnprocessableEntity,
				fmt.Sprintf("the query would scan %s; this endpoint's ceiling is %s", ByteSize(plan.Bytes), maxBytes)
		}
	}

	// ONE MORE ROW THAN THE CEILING, because a reader cannot otherwise tell
	// "exactly the ceiling" from "more than it", and the answer has to say
	// which. MaxBytes travels too: a query priced a moment ago can grow
	// before it runs, and this is what makes the refusal hold anyway.
	req := dialect.Request{Query: query, Args: args, Limit: ceiling + 1, MaxBytes: int64(maxBytes)}
	var res dialect.Result
	run := func(ctx context.Context) error {
		var err error
		res, err = reader.Read(ctx, req)
		return err
	}
	if guards {
		err = guard.ReadOnly(ctx, run)
	} else {
		err = run(ctx)
	}
	if err != nil {
		// NOTHING OF THE QUERY OR ITS VALUES IN THE ANSWER. The caller sent
		// the values and the author wrote the SQL; neither needs a
		// warehouse's error message quoting them back over the wire.
		return dialect.Result{}, http.StatusBadGateway, "the warehouse refused the query"
	}
	return res, 0, ""
}

// sent is the declared parameters a request named, sorted -- the audit
// line's `params`. Undeclared ones are left out: the request is refused for
// them, and their names are the caller's to choose.
func (e *Endpoint) sent(r *http.Request) []string {
	q, _ := url.ParseQuery(r.URL.RawQuery)
	var out []string
	for name := range q {
		if _, declared := e.Params[name]; declared {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Metrics is the Prometheus exposition, for its own port: never the API's,
// which is public by design, and a /metrics on it would publish every
// endpoint's traffic.
func (s *Server) Metrics() http.Handler { return s.met.handler() }

// values turns a query string into what the template is rendered with.
//
// AN UNKNOWN PARAMETER IS A 400 and not ignored: `?campain=x` ignored is a
// report with no filter, returned with a 200 to somebody who believes they
// filtered it. A REPEATED one is a 400 too, because which of the two was
// meant is not a question this can answer.
func (e *Endpoint) values(r *http.Request) (render.Values, error) {
	// ParseQuery AND NOT r.URL.Query(), which drops a pair it cannot parse
	// -- a `;` since Go 1.17 -- and hands back the rest: `?sku=a;b` would
	// become a request with no filter at all, answered with a 200.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("the query string does not parse: %v", err)
	}
	if _, sent := q[tenantName]; sent {
		return nil, fmt.Errorf("`tenant` cannot be sent: it is the key's")
	}
	for name, given := range q {
		if _, declared := e.Params[name]; !declared {
			return nil, fmt.Errorf("unknown parameter %q; this endpoint takes: %s",
				name, orNone(sortedNames(e.Params)))
		}
		if len(given) > 1 {
			return nil, fmt.Errorf("the parameter %s is given %d times", name, len(given))
		}
	}
	vals := render.Values{}
	period := ""
	for _, name := range sortedNames(e.Params) {
		p := e.Params[name]
		raw, sent := "", false
		if given, there := q[name]; there {
			raw, sent = given[0], true
		} else if p.Default != nil {
			raw, sent = *p.Default, true
		}
		if !sent {
			if p.Required {
				return nil, fmt.Errorf("the parameter %s is required", name)
			}
			continue
		}
		v, err := p.parse(name, raw)
		if err != nil {
			return nil, err
		}
		if p.Type == typePeriod {
			period = raw
			continue
		}
		vals[name] = v
	}
	if e.period != "" {
		if err := e.datesFrom(q, period, vals); err != nil {
			return nil, err
		}
	}
	// COMMA-OK: start_date may be declared as something other than a date
	// on an endpoint with no period, and then there is no order to check.
	from, isDate := vals[startDate].Bound().(dialect.Date)
	to, alsoDate := vals[endDate].Bound().(dialect.Date)
	if isDate && alsoDate && from.String() > to.String() {
		return nil, fmt.Errorf("start_date is after end_date")
	}
	return vals, nil
}

// datesFrom sets start_date and end_date from the period, unless the caller
// sent both -- explicit dates win. One without the other is a 400: half a
// range is not a range, and filling the other half from the period would be
// a guess about which half the caller meant.
func (e *Endpoint) datesFrom(q url.Values, period string, vals render.Values) error {
	_, sentStart := q[startDate]
	_, sentEnd := q[endDate]
	switch {
	case sentStart && !sentEnd:
		return fmt.Errorf("start_date is sent without end_date; send both, or neither and a period")
	case sentEnd && !sentStart:
		return fmt.Errorf("end_date is sent without start_date; send both, or neither and a period")
	case sentStart && sentEnd, period == "":
		return nil
	}
	loc, now := e.loc, e.now
	if loc == nil {
		loc = time.UTC
	}
	if now == nil {
		now = time.Now
	}
	from, to, err := resolvePeriod(period, now(), loc)
	if err != nil {
		return fmt.Errorf("the parameter %s is %v", e.period, err)
	}
	vals[startDate], vals[endDate] = render.Bind(from), render.Bind(to)
	return nil
}

// encode writes the answer with each row's columns IN THE ORDER THE QUERY
// NAMED THEM. A Go map marshals sorted, and `day, clicks` arriving as
// `clicks, day` is a different report to whoever reads it.
//
// AND AT MOST maxBytes OF IT, when there is a ceiling. max_rows bounds how
// many rows and nothing bounded how wide -- found in review -- so the
// answer stops at the last row that fits, says it was cut, and counts what
// it kept. The ending is measured at its longest before any row is chosen,
// so the ceiling holds exactly and not roughly. It returns how many rows it
// kept.
func encode(endpoint string, res dialect.Result, truncated bool, maxBytes ByteSize) ([]byte, int, error) {
	at := time.Now().UTC().Format(time.RFC3339)
	ending := func(rows int, cut bool) ([]byte, error) {
		meta, err := json.Marshal(map[string]any{"endpoint": endpoint, "rows": rows, "generated_at": at})
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf(`],"truncated":%t,"meta":%s}`, cut, meta)), nil
	}
	const opening = `{"data":[`
	longest, err := ending(len(res.Rows), false) // "false" is the longer word
	if err != nil {
		return nil, 0, err
	}

	var b bytes.Buffer
	b.WriteString(opening)
	kept := 0
	for _, row := range res.Rows {
		r, err := encodeRow(res.Columns, row)
		if err != nil {
			return nil, 0, err
		}
		size := b.Len() + len(r) + len(longest)
		if kept > 0 {
			size++ // the comma
		}
		if maxBytes > 0 && ByteSize(size) > maxBytes {
			truncated = true
			break
		}
		if kept > 0 {
			b.WriteByte(',')
		}
		b.Write(r)
		kept++
	}
	end, err := ending(kept, truncated)
	if err != nil {
		return nil, 0, err
	}
	b.Write(end)
	return b.Bytes(), kept, nil
}

func encodeRow(columns []string, row []any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for j, col := range columns {
		if j > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(col)
		if err != nil {
			return nil, err
		}
		var cell any
		if j < len(row) {
			cell = row[j]
		}
		v, err := json.Marshal(cell)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func refuse(w http.ResponseWriter, status int, why string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": why})
}

// keyShape is what the cache key holds of an endpoint's after, if any.
func keyShape(e *Endpoint, shape *shaping) string {
	if shape == nil {
		return ""
	}
	return shapeKey(e.afterName, buildID, shape.params)
}
