package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// Request is what a before hook sees of one call. Query is the caller's query
// string, and the only field whose changes are kept: what the hook leaves
// there is validated exactly as if the caller had sent it, so a hook can
// translate a value but cannot slip past a type, a tenant or a declaration.
type Request struct {
	Endpoint string
	Tenant   string
	Query    url.Values
	// Header is a copy, without Authorization.
	Header http.Header
}

// Before runs before the parameters are read. Nil goes on; Refuse(msg)
// answers 400 with msg; any other error answers 500, its text kept for the
// audit line and never sent.
type Before func(ctx context.Context, r *Request) error

// Result is what an after hook sees: the rows, already cut by max_rows.
type Result struct {
	Endpoint string
	Tenant   string
	// Params are the values the SQL was rendered with, typed.
	Params    map[string]any
	Columns   []string
	Rows      [][]any
	Truncated bool
}

// After returns what becomes the answer's `data`, marshalled as JSON.
//
// IT MUST BE A FUNCTION OF ITS RESULT. Its output is what the cache keeps,
// so a hook that reads the clock or the network answers every later caller
// with what was true for the first one.
type After func(ctx context.Context, r *Result) (any, error)

// refusal is a before's word to the caller.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

// Refuse is how a before hook answers 400. The message goes to the caller as
// written, so it is the hook author's to keep free of anything private.
func Refuse(msg string) error { return refusal{msg: msg} }

// Hooks is what this binary knows how to run.
//
// COMPILED IN AND NAMED BY THE FILE, as the gateway's are, and for its
// measurements: a Go function is 93 ns/op against Starlark's 959, nothing is
// added to the module, and `plugin.Open` returns "not implemented" under the
// CGO_ENABLED=0 every image here is built with. The cost, said where it is
// paid: a new hook is a rebuild and a deploy, not a config change.
//
// TWO REGISTRIES, because a before and an after take different arguments: a
// name found in the wrong one is a hook that cannot be called.
type Hooks struct {
	mu     sync.RWMutex
	before map[string]Before
	after  map[string]After
}

func NewHooks() *Hooks {
	return &Hooks{before: map[string]Before{}, after: map[string]After{}}
}

// RegisterBefore adds one. A duplicate name is REFUSED rather than
// overwritten: a silently replaced registration is the wrong hook running in
// production.
func (h *Hooks) RegisterBefore(name string, fn Before) error {
	if fn == nil {
		return fmt.Errorf("the before hook %q is nil", name)
	}
	return register(h, h.before, "before", name, fn)
}

// RegisterAfter adds one, on RegisterBefore's terms.
func (h *Hooks) RegisterAfter(name string, fn After) error {
	if fn == nil {
		return fmt.Errorf("the after hook %q is nil", name)
	}
	return register(h, h.after, "after", name, fn)
}

// MustRegisterBefore is RegisterBefore for a main with nowhere to return an
// error.
func (h *Hooks) MustRegisterBefore(name string, fn Before) {
	if err := h.RegisterBefore(name, fn); err != nil {
		panic(err)
	}
}

// MustRegisterAfter is RegisterAfter for a main with nowhere to return an
// error.
func (h *Hooks) MustRegisterAfter(name string, fn After) {
	if err := h.RegisterAfter(name, fn); err != nil {
		panic(err)
	}
}

func register[F any](h *Hooks, by map[string]F, kind, name string, fn F) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a %s hook needs a name", kind)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, taken := by[name]; taken {
		return fmt.Errorf("a %s hook called %q is already registered", kind, name)
	}
	by[name] = fn
	return nil
}

// HookNames is what an endpoint's `hook:` says: a name in this binary's
// registry for each, both optional.
type HookNames struct {
	Before *string `yaml:"before"`
	After  *string `yaml:"after"`
}

// resolveHooks finds the endpoint's hooks in the registry, at load: a file naming
// a hook nobody registered stops the API from starting, instead of answering
// 500 to every caller of that endpoint for a reason only the log has.
func (e *Endpoint) resolveHooks(h *Hooks) error {
	if e.Hook == nil {
		return nil
	}
	if h == nil {
		h = NewHooks()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	var err error
	if e.Hook.Before != nil {
		if e.before, err = lookup(h.before, e.Name, "before", *e.Hook.Before); err != nil {
			return err
		}
	}
	if e.Hook.After != nil {
		if e.after, err = lookup(h.after, e.Name, "after", *e.Hook.After); err != nil {
			return err
		}
		e.afterName = *e.Hook.After
	}
	return nil
}

func lookup[F any](by map[string]F, endpoint, kind, name string) (F, error) {
	var none F
	// EMPTY IS NOT ABSENT. `before: ""` is a hook somebody meant to name, or
	// one they meant to remove; either way the file should say which.
	if strings.TrimSpace(name) == "" {
		return none, fmt.Errorf("endpoint %s: `hook.%s` is empty; write the name of a registered "+
			"hook, or remove the key", endpoint, kind)
	}
	if fn, found := by[name]; found {
		return fn, nil
	}
	if len(by) == 0 {
		return none, fmt.Errorf("endpoint %s: `hook.%s` is %q, and this binary registers no %s hook. "+
			"Hooks are Go, compiled in: the published brevis-sql-api has none, so a file with `hook:` "+
			"needs a binary of its own -- sql/example is one, ten lines around api.Main",
			endpoint, kind, name, kind)
	}
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	return none, fmt.Errorf("endpoint %s: `hook.%s` is %q, which this binary does not register "+
		"(its %s hooks: %s)", endpoint, kind, name, kind, strings.Join(names, ", "))
}

// runBefore applies the endpoint's before to a copy of what the caller sent,
// and returns the request to read from here on -- or a status, with what to
// tell the caller on a 400 and what to tell the audit line otherwise.
// release is the tenant's slot the hook holds, given back when it returns.
//
// THE KEY NEVER REACHES IT. Authorization is dropped from the copy: a hook
// is the customer's code, and nothing it does needs the bearer it was called
// with.
func runBefore(ctx context.Context, m *metrics, e *Endpoint, tenant string, r *http.Request,
	release func()) (*http.Request, int, string) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		release()
		return nil, http.StatusBadRequest, fmt.Sprintf("the query string does not parse: %v", err)
	}
	header := r.Header.Clone()
	header.Del("Authorization")
	req := &Request{Endpoint: e.Name, Tenant: tenant, Query: q, Header: header}
	started := time.Now()
	_, err = bounded(ctx, "before", func() (struct{}, error) { return struct{}{}, e.before(ctx, req) }, release)
	m.hook(e.Name, "before", outcomeOf(err), time.Since(started))
	if err != nil {
		var said refusal
		var late hookTimeout
		switch {
		case errors.As(err, &said):
			return nil, http.StatusBadRequest, said.msg
		case errors.As(err, &late):
			return nil, http.StatusGatewayTimeout, err.Error()
		}
		return nil, http.StatusInternalServerError, err.Error()
	}
	out := r.Clone(r.Context())
	out.URL.RawQuery = req.Query.Encode()
	return out, 0, ""
}

// bounded runs a hook on a goroutine of its own and waits for it no longer
// than ctx. release is the slot the hook holds, and the goroutine gives it
// back when the hook returns -- in time or not.
//
// THE CONTEXT IS A REQUEST, NOT A GUARANTEE. A hook that never reads it --
// a tight loop, a call with no deadline -- would otherwise hold the request,
// and every request collapsed behind it, for as long as it likes. So the
// request gives up at the timeout and the hook is abandoned; and because an
// abandoned hook keeps its tenant's slot until it really returns, a hook
// that hangs leaves at most four goroutines per tenant behind it, after
// which that tenant is told 429. Not every tenant a 503, and not a process
// that grows until it is killed.
//
// A PANIC IS ONE REQUEST'S ERROR: a hook is ordinary Go, so its failures are
// ordinary too -- a nil map, a bad assertion -- and none is a reason to stop
// serving every other caller.
func bounded[T any](ctx context.Context, kind string, fn func() (T, error), release func()) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if p := recover(); p != nil {
				r = result{err: hookPanic{fmt.Errorf("the %s hook panicked: %v", kind, p)}}
			}
			release()
			done <- r
		}()
		r.v, r.err = fn()
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		var none T
		return none, hookTimeout{fmt.Errorf("the %s hook did not return within the endpoint's timeout, "+
			"and was abandoned", kind)}
	}
}

// shaping is one request's after and the parameters it is shown.
type shaping struct {
	after  After
	params map[string]any
}

// plainParams is what an after is shown of the values the SQL was rendered
// with: every declared one that was sent or defaulted, the tenant excepted --
// that is Result.Tenant.
//
// IN TYPES A BINARY OUTSIDE THIS MODULE CAN NAME. A date arrives as the
// YYYY-MM-DD the caller sent, not as dialect.Date, which is internal and so
// a type the hook's author could not even assert.
func plainParams(vals render.Values) map[string]any {
	out := make(map[string]any, len(vals))
	for name, v := range vals {
		if name == tenantName {
			continue
		}
		p := v.Plain()
		if d, isDate := p.(dialect.Date); isDate {
			p = d.String()
		}
		out[name] = p
	}
	return out
}

// shapeWith runs an after and writes the whole answer around what it
// returns: `data` is the hook's, `truncated` and `meta` stay the API's, so
// every endpoint answers in one envelope.
//
// max_response_bytes IS CHECKED OVER THE WHOLE BODY and refuses rather than
// cuts: the hook's output has no rows to stop at, and half of a shape its
// author chose is not a shape anybody can read.
func shapeWith(ctx context.Context, fn After, r *Result, maxBytes ByteSize, release func()) ([]byte, error) {
	data, err := bounded(ctx, "after", func() (any, error) { return fn(ctx, r) }, release)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("the after hook returned a value that is not JSON: %v", err)
	}
	meta, err := json.Marshal(map[string]any{"endpoint": r.Endpoint, "rows": len(r.Rows),
		"generated_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return nil, err
	}
	body := fmt.Appendf(nil, `{"data":%s,"truncated":%t,"meta":%s}`, encoded, r.Truncated, meta)
	if maxBytes > 0 && ByteSize(len(body)) > maxBytes {
		return nil, oversize{fmt.Errorf("the after hook's answer is %s, over this endpoint's max_response_bytes of %s",
			ByteSize(len(body)), maxBytes)}
	}
	return body, nil
}

// buildID is this binary: the main module's version and, when it was built
// from a repository, the commit and whether the tree was clean.
var buildID = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	id := info.Main.Version
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" || s.Key == "vcs.modified" {
			id += " " + s.Value
		}
	}
	return id
}()

// shapeOf is what the cache key holds of an endpoint's after: its name and
// the build it was compiled in, or nothing when there is no after.
//
// THE BUILD AND NOT ONLY THE NAME. An after's output is cached, and a deploy
// that changes what `to_series` does under the same name would otherwise be
// answered, until the ttl, by what the previous binary made.
func shapeOf(after, build string) string {
	if after == "" {
		return ""
	}
	return after + "@" + build
}

// hookPanic, oversize and hookTimeout are a hook's failures that the
// metrics and the status tell apart from an error it returned.
type hookPanic struct{ error }
type oversize struct{ error }
type hookTimeout struct{ error }

// outcomeOf is a hook's result as a label: a CLOSED SET, never the error's
// text, which is the hook author's and could be anything -- a label is a
// series for as long as the database keeps it.
func outcomeOf(err error) string {
	var said refusal
	var panicked hookPanic
	var over oversize
	var late hookTimeout
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &said):
		return "refused"
	case errors.As(err, &panicked):
		return "panic"
	case errors.As(err, &over):
		return "oversize"
	case errors.As(err, &late):
		return "timeout"
	}
	return "error"
}

// shapeKey is the shape the cache key holds: the after and its build, and
// every parameter it is shown -- or nothing, without an after.
//
// THE PARAMETERS, because an after reads them and the SQL may not: a
// `format` declared for the hook alone never reaches the statement, so the
// statement and its arguments cannot tell `series` from `table`, and the
// second caller would be served the first one's shape. Sorted by name, and
// each part length-prefixed with its Go type, as the arguments are: the int
// 5 and the string "5" are two answers, and no two sets concatenate alike.
func shapeKey(after, build string, params map[string]any) string {
	shape := shapeOf(after, build)
	if shape == "" {
		return ""
	}
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(shape)
	part := func(s string) { fmt.Fprintf(&b, "%d:%s", len(s), s) }
	for _, n := range names {
		part(n)
		part(fmt.Sprintf("%T", params[n]))
		part(fmt.Sprint(params[n]))
	}
	return b.String()
}
