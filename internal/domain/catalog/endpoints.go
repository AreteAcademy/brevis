package catalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Endpoints is what `brevis-sql-api describe` prints: an API file as it
// runs, published as a deliberate act the way a gateway is.
//
// THE ENGINE'S OWN READING OF IT, and not the API's types. The engine
// imports nothing of the `sql` module -- `engine-weight.sh` is the rule --
// so this declares the shape it accepts and refuses everything else.
type Endpoints struct {
	Version    int               `json:"version"`
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Connection string            `json:"connection"`
	Dialect    string            `json:"dialect"`
	Cache      *EndpointsCache   `json:"cache,omitempty"`
	Endpoints  []Endpoint        `json:"endpoints"`
	Notes      map[string]string `json:"notes,omitempty"`
}

// EndpointsCache is the API's cache, by the variable's NAME.
type EndpointsCache struct {
	Type     string `json:"type"`
	AddrFrom string `json:"addr_from"`
	TTL      string `json:"ttl"`
	MaxEntry int64  `json:"max_entry"`
}

// Endpoint is one published path.
type Endpoint struct {
	Name   string            `json:"name"`
	Path   string            `json:"path"`
	Tenant string            `json:"tenant"`
	Params []EndpointParam   `json:"params"`
	Limits EndpointLimits    `json:"limits"`
	Reads  []string          `json:"reads"`
	Notes  map[string]string `json:"notes,omitempty"`
}

// EndpointParam is one parameter as a caller sees it.
type EndpointParam struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Default  *string  `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
	Values   []string `json:"values,omitempty"`
	Min      *int64   `json:"min,omitempty"`
	Max      *int64   `json:"max,omitempty"`
	MaxLen   int      `json:"max_len,omitempty"`
}

// EndpointLimits are an endpoint's effective limits.
type EndpointLimits struct {
	Timeout        string `json:"timeout"`
	MaxRows        int    `json:"max_rows"`
	MaxBytesBilled int64  `json:"max_bytes_billed"`
}

var (
	endpointPath = regexp.MustCompile(`^/v[0-9]+(/[a-z0-9_-]+)+$`)
	variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseEndpoints reads and checks an API's manifest, refusing it WHOLE on the
// first problem and naming where -- by position, never quoting a value that
// carries a credential. The gateway's rules, applied to an API: every
// target is a valid one, every name and note is clean, and a `*_from` holds
// a variable's name and nothing that looks like what the variable holds.
func ParseEndpoints(data []byte) (Endpoints, error) {
	var m Endpoints
	if err := json.Unmarshal(data, &m); err != nil {
		return Endpoints{}, fmt.Errorf("not a manifest: %w", err)
	}
	if m.Kind != "endpoints" {
		return Endpoints{}, fmt.Errorf("kind is %q, want \"endpoints\" -- the output of `brevis-sql-api describe`", m.Kind)
	}
	if m.Version != 1 {
		return Endpoints{}, fmt.Errorf("version %d: this engine reads version 1", m.Version)
	}
	if strings.TrimSpace(m.Name) == "" {
		return Endpoints{}, fmt.Errorf("the manifest names no API")
	}
	for what, v := range map[string]string{"the API's name": m.Name, "the connection": m.Connection, "the dialect": m.Dialect} {
		if err := nameIsClean(what, v); err != nil {
			return Endpoints{}, err
		}
	}
	if c := m.Cache; c != nil {
		if !variableName.MatchString(c.AddrFrom) {
			return Endpoints{}, fmt.Errorf("the cache's addr_from is not a variable's name; it holds the NAME of one")
		}
		for what, v := range map[string]string{"the cache's type": c.Type, "the cache's ttl": c.TTL} {
			if err := nameIsClean(what, v); err != nil {
				return Endpoints{}, err
			}
		}
	}
	if err := cleanNotes("the API", m.Notes); err != nil {
		return Endpoints{}, err
	}
	names, paths := map[string]bool{}, map[string]bool{}
	for i, e := range m.Endpoints {
		where := fmt.Sprintf("endpoint %d", i+1)
		if err := nameIsClean(where+"'s name", e.Name); err != nil {
			return Endpoints{}, err
		}
		if strings.TrimSpace(e.Name) == "" {
			return Endpoints{}, fmt.Errorf("%s has no name", where)
		}
		if !endpointPath.MatchString(e.Path) {
			return Endpoints{}, fmt.Errorf("%s: the path is not a versioned API path like /v1/clicks", where)
		}
		if names[e.Name] || paths[e.Path] {
			return Endpoints{}, fmt.Errorf("%s: its name or its path is published twice", where)
		}
		names[e.Name], paths[e.Path] = true, true
		if e.Tenant != "key" && e.Tenant != "none" {
			return Endpoints{}, fmt.Errorf("%s: tenant is %q; it is `key` or `none`", where, e.Tenant)
		}
		for j, p := range e.Params {
			pw := fmt.Sprintf("%s's parameter %d", where, j+1)
			for _, v := range []string{p.Name, p.Type} {
				if err := nameIsClean(pw, v); err != nil {
					return Endpoints{}, err
				}
			}
			if p.Default != nil {
				if err := FindSecretInText(pw+"'s default", *p.Default); err != nil {
					return Endpoints{}, fmt.Errorf("%s's default carries a credential", pw)
				}
			}
			for _, v := range p.Values {
				if err := nameIsClean(pw+"'s values", v); err != nil {
					return Endpoints{}, err
				}
			}
		}
		if err := nameIsClean(where+"'s timeout", e.Limits.Timeout); err != nil {
			return Endpoints{}, err
		}
		for _, t := range e.Reads {
			if err := ValidTarget(t, false); err != nil {
				return Endpoints{}, fmt.Errorf("%s reads a target that is not one", where)
			}
		}
		if err := cleanNotes(where, e.Notes); err != nil {
			return Endpoints{}, err
		}
	}
	return m, nil
}

func cleanNotes(where string, notes map[string]string) error {
	for path, note := range notes {
		if err := nameIsClean(where+"'s note's setting", path); err != nil {
			return err
		}
		if FindSecretInText("", note) != nil {
			return fmt.Errorf("a note of %s carries a credential", where)
		}
	}
	return nil
}
