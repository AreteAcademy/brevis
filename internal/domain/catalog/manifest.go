package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Manifest is a gateway's published description of where its streams land,
// as `gateway describe` prints it.
//
// The engine reads it and builds nothing from it. Its own types and not the
// gateway's: the gateway imports the SDK, and ./cmd/brevis may not.
type Manifest struct {
	Kind        string           `json:"kind"`
	Version     int              `json:"version"`
	Gateway     string           `json:"gateway"`
	GeneratedAt string           `json:"generated_at"`
	Streams     []ManifestStream `json:"streams"`
}

// ManifestStream is one stream's destinations.
type ManifestStream struct {
	Path         string                `json:"path"`
	Destinations []ManifestDestination `json:"destinations"`
}

// ManifestDestination is one sink of a stream. Target is nil when the gateway
// could not name it; Note says why.
type ManifestDestination struct {
	Role   string  `json:"role"`
	Kind   string  `json:"kind"`
	Target *string `json:"target"`
	Note   string  `json:"note"`
	Routes bool    `json:"routes"`
}

var manifestRoles = map[string]bool{"sink": true, "dead_letter": true, "archive": true}

// ParseManifest reads and checks a manifest, refusing it WHOLE on the first
// problem and naming the stream and role. Half a manifest stored would be a
// console showing half a gateway and saying nothing.
//
// Every target is checked with ValidTarget; a pattern is admitted only on a
// destination that routes.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("not a manifest: %w", err)
	}
	if m.Kind != "gateway-manifest" {
		return Manifest{}, fmt.Errorf("kind is %q, want \"gateway-manifest\" -- the output of `gateway describe`", m.Kind)
	}
	if m.Version != 1 {
		return Manifest{}, fmt.Errorf("version %d: this engine reads version 1", m.Version)
	}
	if strings.TrimSpace(m.Gateway) == "" {
		return Manifest{}, fmt.Errorf("the manifest names no gateway")
	}
	for _, st := range m.Streams {
		if strings.TrimSpace(st.Path) == "" {
			return Manifest{}, fmt.Errorf("a stream has no path")
		}
		for _, d := range st.Destinations {
			where := st.Path + ", " + d.Role
			if !manifestRoles[d.Role] {
				return Manifest{}, fmt.Errorf("%s: unknown role %q (sink, dead_letter, archive)", st.Path, d.Role)
			}
			if strings.TrimSpace(d.Kind) == "" {
				return Manifest{}, fmt.Errorf("%s: no kind", where)
			}
			if d.Target != nil {
				if err := ValidTarget(*d.Target, d.Routes); err != nil {
					return Manifest{}, fmt.Errorf("%s: %w", where, err)
				}
			}
		}
	}
	return m, nil
}
