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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/connections"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// DefaultMaxRows is the row ceiling when the file names none.
const DefaultMaxRows = 10000

// Config is one endpoint file, loaded and checked.
type Config struct {
	Name       string      `yaml:"name"`
	Connection string      `yaml:"connection"`
	Listen     Listen      `yaml:"listen"`
	Endpoints  []*Endpoint `yaml:"endpoints"`

	// Dialect and Open are the connection's, resolved from the registry at
	// load: a file naming a connection nobody declared is refused there,
	// not at the first request.
	Dialect dialect.Dialect                             `yaml:"-"`
	Open    func(context.Context) (dialect.Conn, error) `yaml:"-"`

	// MaxRows is the file-wide row ceiling.
	MaxRows int `yaml:"-"`
}

// Listen is where the API answers.
type Listen struct {
	Addr string `yaml:"addr"`
}

// Endpoint is one declared GET path.
type Endpoint struct {
	Name    string           `yaml:"name"`
	Path    string           `yaml:"path"`
	Params  map[string]Param `yaml:"params"`
	SQL     string           `yaml:"sql"`
	SQLFile string           `yaml:"sql_file"`

	// MaxRows is this endpoint's ceiling; zero takes the file's.
	MaxRows int `yaml:"-"`

	Template *render.Template `yaml:"-"`
}

// Param is one query-string parameter an endpoint takes.
type Param struct {
	Type   string `yaml:"type"`
	MaxLen int    `yaml:"max_len"`
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
	cfg.MaxRows = DefaultMaxRows

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
		if err := e.check(); err != nil {
			return nil, err
		}
		if err := e.parse(cfg.Dialect); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func (e *Endpoint) check() error {
	for _, name := range sortedNames(e.Params) {
		p := e.Params[name]
		if !nameShape.MatchString(name) {
			return fmt.Errorf("endpoint %s: the parameter %q is not a name: lowercase letters, digits and _", e.Name, name)
		}
		switch p.Type {
		case "string":
			if p.MaxLen < 1 || p.MaxLen > maxStringLen {
				return fmt.Errorf("endpoint %s: the parameter %s is a string and needs a `max_len` "+
					"from 1 to %d", e.Name, name, maxStringLen)
			}
		default:
			return fmt.Errorf("endpoint %s: the parameter %s has type %q; the types are: string",
				e.Name, name, p.Type)
		}
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
	for _, name := range tmpl.Names() {
		if _, declared := e.Params[name]; !declared {
			return fmt.Errorf("endpoint %s: the SQL uses `%s`, which is not a declared parameter (declared: %s)",
				e.Name, name, orNone(sortedNames(e.Params)))
		}
	}
	e.Template = tmpl
	return nil
}

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
