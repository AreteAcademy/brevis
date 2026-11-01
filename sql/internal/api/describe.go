package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/AreteAcademy/brevis/sql/internal/connections"
)

// ManifestVersion is the shape `describe` prints. An engine that reads
// another refuses it by number.
const ManifestVersion = 1

// Manifest is an API file as it runs, for the console: what each endpoint
// takes and reads, never how it reads it.
//
// NO SQL TEXT. It is in the customer's repository, and what an operator acts
// on is which models an endpoint reads -- a model's page shows the endpoint
// beside its other consumers. NO VALUE OF ANY VARIABLE: a `*_from` is
// published as the name it holds. AND ONLY THE COMMENTS THEIR AUTHOR MARKED
// with `#:`, the gateway's rule: a comment is free text, and a secret ends
// up in one in ways no pattern recognises.
type Manifest struct {
	Version    int                `json:"version"`
	Kind       string             `json:"kind"`
	Name       string             `json:"name"`
	Connection string             `json:"connection"`
	Dialect    string             `json:"dialect"`
	Cache      *ManifestCache     `json:"cache,omitempty"`
	Endpoints  []ManifestEndpoint `json:"endpoints"`
	Notes      map[string]string  `json:"notes,omitempty"`
}

// ManifestCache is the cache's shape, by the variable's name.
type ManifestCache struct {
	Type     string `json:"type"`
	AddrFrom string `json:"addr_from"`
	TTL      string `json:"ttl"`
	MaxEntry int64  `json:"max_entry"`
}

// ManifestEndpoint is one endpoint, its parameters resolved.
type ManifestEndpoint struct {
	Name   string            `json:"name"`
	Path   string            `json:"path"`
	Tenant string            `json:"tenant"`
	Params []ManifestParam   `json:"params"`
	Limits ManifestLimits    `json:"limits"`
	Reads  []string          `json:"reads"`
	Notes  map[string]string `json:"notes,omitempty"`
}

// ManifestParam is one parameter as a caller sees it.
type ManifestParam struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Default  *string  `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
	Values   []string `json:"values,omitempty"`
	Min      *int64   `json:"min,omitempty"`
	Max      *int64   `json:"max,omitempty"`
	MaxLen   int      `json:"max_len,omitempty"`
}

// ManifestLimits are an endpoint's effective limits.
type ManifestLimits struct {
	Timeout        string `json:"timeout"`
	MaxRows        int    `json:"max_rows"`
	MaxBytesBilled int64  `json:"max_bytes_billed"`
}

// Describe prints the manifest of a loaded file. It OPENS NOTHING: the
// targets come from the registry's declarations, which is why it runs where
// the API's credentials do not -- in a publish Job, beside the gateway's.
func Describe(path string, cfg *Config, reg *connections.Registry) ([]byte, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator's own endpoint file
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	root := mappingOf(&doc)

	m := Manifest{Version: ManifestVersion, Kind: "endpoints", Name: cfg.Name,
		Connection: cfg.Connection, Dialect: cfg.Dialect.Name(), Endpoints: []ManifestEndpoint{}}
	if cfg.Cache != nil {
		m.Cache = &ManifestCache{Type: cfg.Cache.Type, AddrFrom: cfg.Cache.AddrFrom,
			TTL: cfg.Cache.TTL.String(), MaxEntry: int64(cfg.Cache.MaxEntry)}
	}
	m.Notes = topNotes(root)

	listed := sequenceOf(valueOf(root, "endpoints"))
	for i, e := range cfg.Endpoints {
		me := ManifestEndpoint{Name: e.Name, Path: e.Path, Tenant: "key", Params: []ManifestParam{},
			Limits: ManifestLimits{Timeout: e.timeout.String(), MaxRows: e.MaxRows, MaxBytesBilled: int64(e.maxBytes)},
			Reads:  []string{}}
		if e.Tenant == "none" {
			me.Tenant = "none"
		}
		for _, name := range sortedNames(e.Params) {
			p := e.Params[name]
			me.Params = append(me.Params, ManifestParam{Name: name, Type: p.Type, Default: p.Default,
				Required: p.Required, Values: p.Values, Min: p.Min, Max: p.Max, MaxLen: p.MaxLen})
		}
		for _, ref := range e.reads {
			if target, found := reg.TargetOf(cfg.Connection, ref); found {
				me.Reads = append(me.Reads, target)
			}
		}
		if i < len(listed) {
			me.Notes = notesOf(listed[i])
		}
		m.Endpoints = append(m.Endpoints, me)
	}

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func mappingOf(doc *yaml.Node) *yaml.Node {
	if doc != nil && doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func valueOf(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for j := 0; j+1 < len(n.Content); j += 2 {
		if n.Content[j].Value == key {
			return n.Content[j+1]
		}
	}
	return nil
}

func sequenceOf(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// topNotes is the file-level settings' notes, by their key -- endpoints
// excluded, which carry their own.
func topNotes(root *yaml.Node) map[string]string {
	out := map[string]string{}
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for j := 0; j+1 < len(root.Content); j += 2 {
		key, val := root.Content[j], root.Content[j+1]
		if key.Value == "endpoints" {
			continue
		}
		collect(key, val, key.Value, out)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// notesOf is one endpoint's notes: the comment above `- name:` under "",
// and every setting's by its path.
func notesOf(n *yaml.Node) map[string]string {
	out := map[string]string{}
	if t := commentText(n.HeadComment); t != "" {
		out[""] = t
	}
	if n.Kind == yaml.MappingNode {
		for j := 0; j+1 < len(n.Content); j += 2 {
			collect(n.Content[j], n.Content[j+1], n.Content[j].Value, out)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collect(key, val *yaml.Node, path string, out map[string]string) {
	var parts []string
	for _, c := range []string{key.HeadComment, key.LineComment, val.LineComment} {
		if t := commentText(c); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) > 0 {
		out[path] = strings.Join(parts, "\n\n")
	}
	if val.Kind == yaml.MappingNode {
		for j := 0; j+1 < len(val.Content); j += 2 {
			collect(val.Content[j], val.Content[j+1], path+"."+val.Content[j].Value, out)
		}
	}
}

// noteMarker is how an author says a comment is meant for the console: `#:`.
// OPT-IN, the gateway's rule and for its reason -- an ordinary comment never
// leaves the file -- and the engine checks what a marked one says again.
const noteMarker = "#:"

// commentText is a comment's marked lines, without the marker and the space
// after it. A bare `#:` is a blank line between paragraphs.
func commentText(c string) string {
	if c == "" {
		return ""
	}
	var kept []string
	for _, l := range strings.Split(c, "\n") {
		text, marked := strings.CutPrefix(strings.TrimSpace(l), noteMarker)
		if marked {
			kept = append(kept, strings.TrimPrefix(text, " "))
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
