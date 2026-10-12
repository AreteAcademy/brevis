// Package api is `brevis-sql-api`: declared, read-only SQL served over HTTP.
//
// THE FILE IS THE CONTRACT, as it is for the gateway: every endpoint, its
// parameters and its SQL, refused at load when any of it cannot be honoured.
// A server that starts on a file it half understood answers a readiness probe
// and then a caller's first request with something nobody declared.
package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/AreteAcademy/brevis/sql/internal/api/cache"
	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/connections"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/refs"
)

// DefaultMaxRows is the row ceiling when the file names none.
const DefaultMaxRows = 10000

// Config is one endpoint file, loaded and checked.
type Config struct {
	Name       string `yaml:"name"`
	Connection string `yaml:"connection"`

	// Timezone is the IANA zone `period` resolves in. UTC when absent.
	Timezone  string      `yaml:"timezone"`
	Listen    Listen      `yaml:"listen"`
	Endpoints []*Endpoint `yaml:"endpoints"`

	// Params are definitions an endpoint's parameter can name with `ref`.
	Params map[string]Param `yaml:"params"`

	// Limits bound every request; see Limits.
	Limits Limits `yaml:"limits"`

	// Cache keeps answers until a model they read is rebuilt. Absent, every
	// request reads the warehouse.
	Cache *CacheConfig `yaml:"cache"`

	// CacheKV is where the cache is kept; nil opens Cache.AddrFrom.
	CacheKV cache.KV `yaml:"-"`

	// Dialect and Open are the connection's, resolved from the registry at
	// load: a file naming a connection nobody declared is refused there,
	// not at the first request.
	Dialect dialect.Dialect                             `yaml:"-"`
	Open    func(context.Context) (dialect.Conn, error) `yaml:"-"`

	// MaxRows is the file-wide row ceiling.
	MaxRows int `yaml:"-"`

	// Metrics is where the Prometheus exposition listens.
	Metrics struct {
		Addr string `yaml:"addr"`
	} `yaml:"metrics"`

	// Audit receives one JSON line per request. Nil writes none.
	Audit io.Writer `yaml:"-"`

	// Env is BREVIS_ENV, read by the binary and never from the file: a file
	// that could declare itself local would be a file that turns off
	// authentication.
	Env string `yaml:"-"`
}

// Listen is where the API answers.
type Listen struct {
	Addr string `yaml:"addr"`
	Auth *Auth  `yaml:"auth"`
}

// Endpoint is one declared GET path.
type Endpoint struct {
	Name    string           `yaml:"name"`
	Path    string           `yaml:"path"`
	Params  map[string]Param `yaml:"params"`
	SQL     string           `yaml:"sql"`
	SQLFile string           `yaml:"sql_file"`

	// Limits are this endpoint's, each at most the file's.
	Limits *Limits `yaml:"limits"`

	// Tenant is `none` for an endpoint whose data is the same for every
	// tenant. Absent, the SQL must use `{{ tenant }}`.
	Tenant string `yaml:"tenant"`

	// MaxRows is this endpoint's ceiling; zero takes the file's.
	MaxRows int `yaml:"-"`

	Template *render.Template `yaml:"-"`

	// period is the name of the endpoint's period parameter, if it has one.
	period string
	// loc and now are when "today" is. Nil means UTC and time.Now.
	loc *time.Location
	now func() time.Time

	// reads are the models the SQL can read, in any branch: what the cache
	// key takes a generation of.
	reads []string

	// timeout and maxBytes are the effective limits; zero takes the file's.
	timeout     time.Duration
	maxBytes    ByteSize
	maxResponse ByteSize
}

// maxStringLen bounds `max_len` itself. A parameter is a filter, not a
// document; anything longer is a body, and this API reads none.
const maxStringLen = 1024

var (
	// A path is versioned from the first day. `/clicks` today is a path that
	// cannot change shape tomorrow without breaking whoever calls it.
	pathShape = regexp.MustCompile(`^/v[0-9]+(/[a-z0-9_-]+)+$`)
	nameShape = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// Load reads an endpoint file and resolves its connection in the registry.
func Load(path string, reg *connections.Registry) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the endpoint file could not be read: %w", err)
	}
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// AN UNKNOWN KEY IS REFUSED. `lisen:` accepted and ignored is a listen
	// address nobody set and a default nobody chose.
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}

	if cfg.Connection == "" {
		return nil, fmt.Errorf("`connection` is required: the name of a connection in the registry (declared: %s)",
			declared(reg))
	}
	d, open, found := reg.Named(cfg.Connection)
	if !found {
		return nil, fmt.Errorf("`connection` is %q, and the registry declares: %s", cfg.Connection, declared(reg))
	}
	cfg.Dialect, cfg.Open = d, open
	if err := cfg.Limits.check("the file's"); err != nil {
		return nil, err
	}
	cfg.Limits = cfg.Limits.withDefaults()
	cfg.MaxRows = cfg.Limits.MaxRows

	if cfg.Cache != nil {
		if err := cfg.Cache.check(); err != nil {
			return nil, err
		}
	}
	if cfg.Listen.Auth != nil {
		if err := cfg.Listen.Auth.check(); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedNames(cfg.Params) {
		p := cfg.Params[name]
		if p.Ref != "" {
			return nil, fmt.Errorf("`params.%s` is a ref; a definition in `params:` is written out", name)
		}
		if err := p.check(); err != nil {
			return nil, fmt.Errorf("`params.%s`: %v", name, err)
		}
	}
	loc := time.UTC
	if cfg.Timezone != "" {
		if loc, err = time.LoadLocation(cfg.Timezone); err != nil {
			return nil, fmt.Errorf("`timezone` is %q, which is not an IANA zone (like America/Sao_Paulo)", cfg.Timezone)
		}
	}
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("the file declares no `endpoints`")
	}
	dir := filepath.Dir(path)
	names, paths := map[string]bool{}, map[string]bool{}
	for _, e := range cfg.Endpoints {
		if e.Name == "" || !nameShape.MatchString(e.Name) {
			return nil, fmt.Errorf("an endpoint's `name` is %q: lowercase letters, digits and _", e.Name)
		}
		if names[e.Name] {
			return nil, fmt.Errorf("the endpoint %q is declared twice", e.Name)
		}
		names[e.Name] = true
		if !pathShape.MatchString(e.Path) {
			return nil, fmt.Errorf("endpoint %s: `path` is %q; a path is versioned and lowercase, like /v1/clicks",
				e.Name, e.Path)
		}
		if paths[e.Path] {
			return nil, fmt.Errorf("endpoint %s: the path %s is declared twice", e.Name, e.Path)
		}
		paths[e.Path] = true

		switch {
		case e.SQL != "" && e.SQLFile != "":
			return nil, fmt.Errorf("endpoint %s has both `sql` and `sql_file`; keep one", e.Name)
		case e.SQL == "" && e.SQLFile == "":
			return nil, fmt.Errorf("endpoint %s has neither `sql` nor `sql_file`", e.Name)
		case e.SQLFile != "":
			// RELATIVE TO THE FILE, not to wherever the binary was started:
			// the two travel together, in a ConfigMap or a repository.
			body, err := os.ReadFile(filepath.Join(dir, e.SQLFile))
			if err != nil {
				return nil, fmt.Errorf("endpoint %s: `sql_file` %s could not be read: %w", e.Name, e.SQLFile, err)
			}
			e.SQL = string(body)
		}
		if err := e.resolve(cfg.Params); err != nil {
			return nil, err
		}
		if err := e.check(); err != nil {
			return nil, err
		}
		if err := e.parse(cfg.Dialect); err != nil {
			return nil, err
		}
		e.loc = loc
		own, err := cfg.Limits.lowered(e.Name, e.Limits)
		if err != nil {
			return nil, err
		}
		e.MaxRows, e.timeout, e.maxBytes = own.MaxRows, own.Timeout, own.MaxBytesBilled
		e.maxResponse = own.MaxResponseBytes
	}
	return cfg, nil
}

func (e *Endpoint) check() error {
	for _, name := range sortedNames(e.Params) {
		p := e.Params[name]
		if name == tenantName {
			return fmt.Errorf("endpoint %s: `tenant` is reserved -- it is the key's, bound into "+
				"every query, and never a parameter a caller sends", e.Name)
		}
		if !nameShape.MatchString(name) {
			return fmt.Errorf("endpoint %s: the parameter %q is not a name: lowercase letters, digits and _", e.Name, name)
		}
		if err := p.check(); err != nil {
			return fmt.Errorf("endpoint %s, parameter %s: %v", e.Name, name, err)
		}
	}
	return e.checkPeriod()
}

// resolve replaces each `{ref: name}` with the file's definition of it.
//
// COPIED, not shared: a map value is a copy in Go anyway, and the point is
// that no endpoint can reach into another's parameter.
func (e *Endpoint) resolve(defs map[string]Param) error {
	for name, p := range e.Params {
		if p.Ref == "" {
			continue
		}
		if !p.onlyRef() {
			return fmt.Errorf("endpoint %s, parameter %s: a `ref` takes nothing else -- "+
				"write the whole definition instead", e.Name, name)
		}
		def, found := defs[p.Ref]
		if !found {
			return fmt.Errorf("endpoint %s, parameter %s: `ref` is %q, which the file's `params:` "+
				"does not define (defined: %s)", e.Name, name, p.Ref, orNone(sortedNames(defs)))
		}
		e.Params[name] = def
	}
	return nil
}

// parse reads the endpoint's template and checks every name it uses was
// declared -- `{{ campain }}` is a typo, and at runtime it would be a value
// nobody can ever send.
func (e *Endpoint) parse(d dialect.Dialect) error {
	tmpl, err := render.Parse(d.Name(), e.SQL)
	if err != nil {
		return fmt.Errorf("endpoint %s: %v", e.Name, err)
	}
	// THE TENANT FILTER IS UNCONDITIONAL. A `{{ tenant }}` only inside a
	// branch is a filter the caller decides about -- by leaving out the
	// parameter that opens the branch -- and `tenant` as a condition is a
	// test that is always true. Found in review, and the reason this asks
	// the template where the name sits rather than whether it is there.
	if tmpl.IsCondition(tenantName) {
		return fmt.Errorf("endpoint %s: `tenant` is tested by a condition; it is always there, "+
			"and the filter on it must not depend on anything", e.Name)
	}
	usesTenant := tmpl.Unconditional(tenantName)
	for _, name := range tmpl.Names() {
		if name == tenantName {
			if !usesTenant {
				return fmt.Errorf("endpoint %s: `{{ tenant }}` is written only inside a condition, so "+
					"a caller who leaves that condition's parameter out reads every tenant's rows. "+
					"Write the tenant filter outside every `{%% if %%}`", e.Name)
			}
			continue
		}
		if e.Params[name].Type == typePeriod {
			return fmt.Errorf("endpoint %s: `{{ %s }}` is a period, which is not a value -- it sets "+
				"start_date and end_date, and the SQL uses those", e.Name, name)
		}
		if _, declared := e.Params[name]; !declared {
			return fmt.Errorf("endpoint %s: the SQL uses `%s`, which is not a declared parameter (declared: %s)",
				e.Name, name, orNone(sortedNames(e.Params)))
		}
	}
	// AN ENDPOINT THAT IGNORES THE TENANT IS A LEAK WAITING FOR A SECOND
	// CUSTOMER. It reads correctly with one tenant in the table, passes every
	// test written that day, and starts returning somebody else's rows the
	// day the second one lands. So it is refused unless the author says, in
	// the file, that the data is the same for everybody.
	switch {
	case e.Tenant != "" && e.Tenant != "none":
		return fmt.Errorf("endpoint %s: `tenant` is %q; the only value it takes is `none`", e.Name, e.Tenant)
	case !usesTenant && e.Tenant != "none":
		return fmt.Errorf("endpoint %s: the SQL never uses `{{ tenant }}`, so every key would read "+
			"every tenant's rows. Filter by it, or write `tenant: none` if the data is the same "+
			"for everybody", e.Name)
	case usesTenant && e.Tenant == "none":
		return fmt.Errorf("endpoint %s says `tenant: none` and its SQL uses `{{ tenant }}`; "+
			"one of them is wrong", e.Name)
	}
	reads, err := refs.Of(d.Name(), tmpl.AllText())
	if err != nil {
		return fmt.Errorf("endpoint %s: the relations its SQL reads cannot be told: %v", e.Name, err)
	}
	e.Template, e.reads = tmpl, reads
	return nil
}

// CacheConfig is where answers are kept, and for how long at most.
type CacheConfig struct {
	Type     string        `yaml:"type"`
	AddrFrom string        `yaml:"addr_from"`
	TTL      time.Duration `yaml:"ttl"`
	MaxEntry ByteSize      `yaml:"max_entry"`
}

const (
	defaultMaxEntry = ByteSize(4 << 20)
	maxMaxEntry     = ByteSize(64 << 20)
)

// check refuses a cache this API cannot keep.
//
// REDIS ONLY: memcached has no MGET over the generations that is atomic
// with anything, and the gateway's memory type is per replica -- a cache
// that N replicas each hold is N answers to one question.
func (c *CacheConfig) check() error {
	if c.Type != "redis" {
		return fmt.Errorf("`cache.type` is %q; the cache is redis", c.Type)
	}
	if c.AddrFrom == "" {
		return fmt.Errorf("`cache.addr_from` is required: the NAME of the variable holding the Redis address")
	}
	// ttl is the CEILING on a cached answer, not the mechanism that
	// refreshes it.
	//
	// ZERO IS REFUSED, and the gateway's metastore is why it has to be said:
	// there `ttl: 0` means "never expires", which is right for what is known
	// about a table and wrong for a result. A result that never expires is a
	// stale answer nobody sees -- the generation is what refreshes it, and
	// the ceiling is what bounds the damage on the day a build forgets to
	// write one.
	if c.TTL <= 0 {
		return fmt.Errorf("`cache.ttl` is %s: a cached answer needs a ceiling. Write one "+
			"(`6h` is a good start), or remove `cache:` to always read the warehouse", c.TTL)
	}
	if c.MaxEntry == 0 {
		c.MaxEntry = defaultMaxEntry
	}
	if c.MaxEntry < 0 || c.MaxEntry > maxMaxEntry {
		return fmt.Errorf("`cache.max_entry` is %s; at most %s", c.MaxEntry, maxMaxEntry)
	}
	return nil
}

// tenantName is the name the key's tenant is bound under.
const tenantName = "tenant"

func sortedNames(params map[string]Param) []string {
	out := make([]string, 0, len(params))
	for n := range params {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func declared(reg *connections.Registry) string { return orNone(reg.Names()) }

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
