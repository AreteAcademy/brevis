package catalog

import (
	"crypto/sha256"
	"encoding/hex"
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

// ManifestStream is one stream: its destinations and, from version 2, its
// configuration.
type ManifestStream struct {
	Name string `json:"name"`
	Path string `json:"path"`

	// Fields are the stream's settings as it runs, in the config's order.
	// Empty in a version-1 manifest, which carried none.
	Fields []ManifestField `json:"fields"`

	// Notes are the config file's comments, by setting path; "" is the
	// stream's own.
	Notes map[string]string `json:"notes"`

	Destinations []ManifestDestination `json:"destinations"`
}

// ManifestField is one setting. Value is what JSON gives back: a string, a
// float64, a bool, or a list of them.
type ManifestField struct {
	Path    string `json:"path"`
	Value   any    `json:"value"`
	Default bool   `json:"default"`
}

// ConfigHash is the stream's configuration's identity, and the body it was
// computed over -- what writer_configs stores under it. Empty for a stream
// with no fields.
//
// THE NOTES ARE NOT IN IT. A comment reworded is the same configuration, and
// a new version for it would bury the change that matters under ones that do
// not (decided with the owner, 2026-10-11).
//
// Computed HERE and not taken from the gateway: the hash is a key in this
// engine's database, and a key a client chooses is a key a client can collide.
func (s ManifestStream) ConfigHash() (string, []byte) { return FieldsHash(s.Fields) }

// FieldsHash is a configuration's identity and the body it was computed over,
// whoever described it -- a gateway's stream or an SDK pipeline. Empty for no
// fields.
func FieldsHash(fields []ManifestField) (string, []byte) {
	if len(fields) == 0 {
		return "", nil
	}
	// A slice of structs whose values are JSON scalars or lists of them:
	// encoding/json writes that the same way every time.
	body, err := json.Marshal(fields)
	if err != nil {
		return "", nil
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), body
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
	if m.Version != 1 && m.Version != 2 {
		return Manifest{}, fmt.Errorf("version %d: this engine reads versions 1 and 2", m.Version)
	}
	if strings.TrimSpace(m.Gateway) == "" {
		return Manifest{}, fmt.Errorf("the manifest names no gateway")
	}
	if err := nameIsClean("the gateway's name", m.Gateway); err != nil {
		return Manifest{}, err
	}
	for i, st := range m.Streams {
		if strings.TrimSpace(st.Path) == "" {
			return Manifest{}, fmt.Errorf("a stream has no path")
		}
		// Before anything below quotes the path in an error.
		stream := fmt.Sprintf("stream %d", i+1)
		if err := nameIsClean(stream+"'s path", st.Path); err != nil {
			return Manifest{}, err
		}
		if err := nameIsClean(stream+"'s name", st.Name); err != nil {
			return Manifest{}, err
		}
		if err := checkConfiguration(st); err != nil {
			return Manifest{}, fmt.Errorf("%s: %w", st.Path, err)
		}
		for j, d := range st.Destinations {
			// The role, kind and note reach the page beside the target, and
			// the errors below quote the role.
			dest := fmt.Sprintf("%s's destination %d", stream, j+1)
			if err := nameIsClean(dest+"'s role", d.Role); err != nil {
				return Manifest{}, err
			}
			if err := nameIsClean(dest+"'s kind", d.Kind); err != nil {
				return Manifest{}, err
			}
			if err := nameIsClean(dest+"'s note", d.Note); err != nil {
				return Manifest{}, err
			}
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

// checkConfiguration holds a stream's settings and notes to what the console
// can show: one value per path, each a scalar or a list of scalars, and no
// credential anywhere (FindSecret, FindSecretInText).
func checkConfiguration(st ManifestStream) error {
	if err := CheckFields(st.Fields); err != nil {
		return err
	}
	for path, note := range st.Notes {
		if err := nameIsClean("a note's setting", path); err != nil {
			return err
		}
		if err := FindSecretInText(joinPath("notes", path), note); err != nil {
			return err
		}
	}
	return nil
}

// CheckFields holds a configuration's settings to what the console can show,
// whoever described them.
func CheckFields(fields []ManifestField) error {
	seen := map[string]bool{}
	for i, f := range fields {
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("a setting has no path")
		}
		if err := nameIsClean(fmt.Sprintf("setting %d's path", i+1), f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("%s: described twice", f.Path)
		}
		seen[f.Path] = true
		if !scalarOrList(f.Value) {
			return fmt.Errorf("%s: a value is a string, a number, a bool or a list of them", f.Path)
		}
		if err := FindSecret(f.Path, f.Value); err != nil {
			return err
		}
	}
	return nil
}

func scalarOrList(v any) bool {
	switch v := v.(type) {
	case nil, string, float64, bool:
		return true
	case []any:
		for _, e := range v {
			switch e.(type) {
			case nil, string, float64, bool:
			default:
				return false
			}
		}
		return true
	}
	return false
}

// nameIsClean refuses a name -- a setting's path, a stream's name, a note's
// key -- carrying a credential's shape. A name is on the page as much as a
// value is.
//
// The refusal says WHERE by position and never quotes the name: an error that
// repeated it would hand the credential on to whoever reads the log.
func nameIsClean(where, name string) error {
	if FindSecretInText("", name) != nil {
		return fmt.Errorf("%s carries a credential", where)
	}
	return nil
}
