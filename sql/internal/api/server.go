package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// Server answers the endpoints of one file.
type Server struct {
	cfg    *Config
	binder dialect.Binder
	mux    *http.ServeMux
	keys   *keyring
	slots  slots
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
	s := &Server{cfg: cfg, binder: b, mux: http.NewServeMux(), keys: keys}
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	endpoints := http.NewServeMux()
	for _, e := range cfg.Endpoints {
		endpoints.Handle(e.Path, s.endpoint(e))
	}
	s.mux.Handle("/", guard(keys, func(w http.ResponseWriter, r *http.Request, tenant string) {
		endpoints.ServeHTTP(w, r.WithContext(withTenant(r.Context(), tenant)))
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		vals, err := e.values(r)
		if err != nil {
			refuse(w, http.StatusBadRequest, err.Error())
			return
		}
		vals[tenantName] = render.Bind(tenant)
		query, args, err := e.Template.Render(vals, s.binder)
		if err != nil {
			refuse(w, http.StatusBadRequest, err.Error())
			return
		}

		release, free := s.slots.take(tenant)
		if !free {
			w.Header().Set("Retry-After", "1")
			refuse(w, http.StatusTooManyRequests, fmt.Sprintf(
				"%d requests from this key's tenant are already waiting on the warehouse", perTenantInFlight))
			return
		}
		defer release()

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		res, status, why := s.read(ctx, query, args, ceiling, maxBytes)
		if status != 0 {
			if ctx.Err() == context.DeadlineExceeded {
				status, why = http.StatusGatewayTimeout,
					fmt.Sprintf("the query took longer than %s and was cancelled", timeout)
			}
			refuse(w, status, why)
			return
		}
		truncated := res.Truncated
		if len(res.Rows) > ceiling {
			res.Rows, truncated = res.Rows[:ceiling], true
		}
		body, err := encode(e.Name, res, truncated)
		if err != nil {
			refuse(w, http.StatusInternalServerError, "the answer could not be written as JSON")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
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
func encode(endpoint string, res dialect.Result, truncated bool) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"data":[`)
	for i, row := range res.Rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('{')
		for j, col := range res.Columns {
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
	}
	meta, err := json.Marshal(map[string]any{
		"endpoint":     endpoint,
		"rows":         len(res.Rows),
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, `],"truncated":%t,"meta":%s}`, truncated, meta)
	return b.Bytes(), nil
}

func refuse(w http.ResponseWriter, status int, why string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": why})
}
