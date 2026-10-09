// Package connections is the warehouses Brevis may reach, declared once.
//
// THE ECOSYSTEM'S REGISTRY AND NOT THIS SERVICE'S. A client who taught the
// SDK to load into a warehouse has already said where that warehouse is; a
// second file saying it again for `/data` would be a second thing to keep
// right, and the one that drifts is the one nobody reads until a preview is
// empty. So the file is `brevis.yaml` at the project's root, `serve` reads
// it today, and the SDK and the gateway read it afterwards.
//
// WHAT IT HOLDS AND WHAT IT NEVER HOLDS: a name, a dialect, and where to
// FIND a credential -- never the credential. `dsn_from` takes the NAME of an
// environment variable, which is the rule `--dsn-from` already follows and
// the gateway's own `dsn_from` before it. The file is in git; a password
// must not be.
package connections

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// Connection is one declared warehouse.
type Connection struct {
	Name    string `yaml:"name"`
	Dialect string `yaml:"dialect"`

	// Project is a BigQuery project id, which IS the connection there: ADC
	// does the rest and there is no DSN to hold.
	Project string `yaml:"project"`

	// DSNFrom is the NAME of the environment variable holding a connection
	// string. Never the string.
	DSNFrom string `yaml:"dsn_from"`

	// key is what a target's first segment is matched against: the project
	// for BigQuery, the database for Postgres. Derived and never declared --
	// a database written beside a DSN is a second statement of the same
	// fact, and the two disagree the day somebody edits one.
	key string

	// dsn is resolved at boot and never leaves this package.
	dsn string
}

// Registry is every connection, in the order they were declared.
type Registry struct{ conns []Connection }

// dialects is what this binary can actually open. A map rather than a switch
// so a refusal can list what there is.
var dialects = map[string]dialect.Dialect{
	postgres.Dialect{}.Name(): postgres.Dialect{},
	bigquery.Dialect{}.Name(): bigquery.Dialect{},
}

// Load reads the registry, or refuses it by name.
//
// REFUSED AT BOOT AND NOT AT THE FIRST REQUEST. A service that accepted a
// broken declaration would answer a readiness probe and then fail the first
// person who opened a tab, which is a lie told to an orchestrator.
//
// A FILE THAT IS NOT THERE IS NOT A FAILURE: `serve` with no registry is
// BigQuery-only, which is what V1 and V2 shipped, and refusing to start
// without a file nobody has written yet would be a service nobody can run.
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the connection registry could not be read: %w", err)
	}

	var file struct {
		Connections []Connection `yaml:"connections"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		// The parser's message can quote the line it choked on, and a line
		// of this file may be a `dsn_from` somebody pasted a DSN into.
		return nil, fmt.Errorf("%s is not a file this can read: its `connections:` block does not parse", path)
	}

	r := &Registry{}
	seen := map[string]bool{}
	for i := range file.Connections {
		c := file.Connections[i]
		if err := c.check(); err != nil {
			return nil, err
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("the connection %q is declared twice, and a name has to say which one", c.Name)
		}
		seen[c.Name] = true
		if err := c.resolve(); err != nil {
			return nil, err
		}
		r.conns = append(r.conns, c)
	}
	return r, nil
}

// check refuses a declaration on its own terms, before anything is resolved.
func (c Connection) check() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("a connection with no `name`, and a target is matched to one by name")
	}
	if _, ok := dialects[c.Dialect]; !ok {
		return fmt.Errorf("the connection %q asks for the dialect %q, and this binary carries %s",
			c.Name, c.Dialect, known())
	}
	if c.Project != "" && c.DSNFrom != "" {
		return fmt.Errorf("the connection %q declares both a `project` and a `dsn_from`, "+
			"and only one of them can be where it connects", c.Name)
	}
	// REFUSED BY NAME RATHER THAN USED. A DSN here is a password in a file
	// that is in git, and treating it as a variable name would fail later
	// with "not set", pointing at the wrong thing. The value is never
	// repeated.
	if strings.ContainsAny(c.DSNFrom, ":/@ ") {
		return fmt.Errorf("the connection %q gives `dsn_from` what looks like a connection string. "+
			"It takes the NAME of an environment variable holding one; the value is not "+
			"repeated here, because this file is in git", c.Name)
	}
	switch c.Dialect {
	case bigquery.Dialect{}.Name():
		if c.Project == "" {
			return fmt.Errorf("the connection %q is BigQuery and declares no `project`, "+
				"which is what a target names and what ADC connects to", c.Name)
		}
	default:
		if c.DSNFrom == "" {
			return fmt.Errorf("the connection %q declares no `dsn_from`, which is the NAME of "+
				"the environment variable holding its connection string", c.Name)
		}
	}
	return nil
}

// resolve reads the credential and derives what a target is matched against.
func (c *Connection) resolve() error {
	if c.Project != "" {
		c.key = c.Project
		return nil
	}
	dsn := os.Getenv(c.DSNFrom)
	if dsn == "" {
		return fmt.Errorf("the connection %q names %s and %s is not set in this environment",
			c.Name, c.DSNFrom, c.DSNFrom)
	}
	// THE DATABASE IS READ OUT OF THE DSN, not declared beside it. A target
	// carries the database the dialect wrote into the catalog, so the match
	// has to be against the same fact -- and a second declaration is a
	// second thing that can disagree.
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// pgx echoes the connection string in some of its errors, and this
		// one reaches a terminal.
		return fmt.Errorf("the connection %q holds something that is not a connection string", c.Name)
	}
	c.key = cfg.Database
	c.dsn = dsn
	return nil
}

// Match finds the connection a target names, and NOT FINDING ONE IS AN
// ANSWER. The screen has to say which connection is missing, and an error
// carries nothing to put on it.
func (r *Registry) Match(d, first string) (Connection, bool) {
	for _, c := range r.conns {
		if c.Dialect == d && c.key == first {
			return c, true
		}
	}
	return Connection{}, false
}

// Open connects to the warehouse a target names.
func (r *Registry) Open(ctx context.Context, d, first string) (dialect.Conn, error) {
	c, found := r.Match(d, first)
	if !found {
		return nil, fmt.Errorf("no connection is declared for that destination")
	}
	where := c.dsn
	if where == "" {
		where = c.Project
	}
	return dialects[c.Dialect].Open(ctx, where)
}

// Names lists the declared connections, for a refusal to name what there is.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.conns))
	for _, c := range r.conns {
		out = append(out, c.Name)
	}
	return out
}

func known() string {
	names := make([]string, 0, len(dialects))
	for n := range dialects {
		names = append(names, n)
	}
	// Sorted, so a refusal reads the same twice: a map's order is not one.
	sort.Strings(names)
	return strings.Join(names, " and ")
}
