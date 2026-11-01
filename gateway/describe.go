package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// ErrNoIdentity is a locate function's way of saying "this destination exists,
// and nothing in the configuration names it". The manifest lists it with no
// target and the reason, rather than failing or inventing a name.
var ErrNoIdentity = errors.New("no identity")

// Manifest is what `gateway describe` prints and `brevis gateway publish`
// stores: where each stream of this gateway lands, by name, never by address,
// and -- from version 2 -- how each stream is configured.
type Manifest struct {
	Kind        string           `json:"kind"`
	Version     int              `json:"version"`
	Gateway     string           `json:"gateway"`
	GeneratedAt string           `json:"generated_at"`
	Streams     []ManifestStream `json:"streams"`
}

// ManifestStream is one stream: its destinations, and its configuration.
type ManifestStream struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`

	// Fields is the stream as it RUNS, defaults filled in, in the order the
	// config declares them. See describeFields.
	Fields []ManifestField `json:"fields,omitempty"`

	// Notes are the file's comments, by the path of the setting they sit on.
	// The comments are where a config says WHY, and a parser drops them.
	Notes map[string]string `json:"notes,omitempty"`

	Destinations []ManifestDestination `json:"destinations"`
}

// ManifestDestination is one sink of a stream.
type ManifestDestination struct {
	// Role is `sink`, `dead_letter`, or `archive` (where an oversized event's
	// body is parked).
	Role string `json:"role"`

	// Kind is the sink's type as the config spells it.
	Kind string `json:"kind"`

	// Target is nil when the destination cannot be named: no locator in this
	// binary, or nothing in the config that identifies it. Note says which.
	Target *string `json:"target"`
	Note   string  `json:"note,omitempty"`

	// Routes marks a pattern: tables this sink learns from the events land
	// under it.
	Routes bool `json:"routes,omitempty"`
}

// Describe builds the manifest from a loaded config. It constructs no sink and
// opens nothing -- see LocateFunc -- so it runs where the gateway's credentials
// do not.
//
// name overrides the config's own `name:`; empty keeps it.
func Describe(cfg *Config, sinks *Sinks, name string, now time.Time) (*Manifest, error) {
	if name == "" {
		name = cfg.Name
	}
	if name == "" {
		return nil, fmt.Errorf("the gateway has no name: set `name:` in the config or pass --name")
	}
	if sinks == nil {
		sinks = NewSinks()
	}
	m := &Manifest{Kind: "gateway-manifest", Version: 2, Gateway: name,
		GeneratedAt: now.UTC().Format(time.RFC3339)}

	for i, st := range cfg.Streams {
		node := streamNode(cfg.doc, i)
		ms := ManifestStream{Name: st.Name, Path: st.Path,
			Fields: describeFields(st, node), Notes: describeNotes(node)}
		roles := []struct {
			role string
			sink Sink
		}{{"sink", st.Sink}, {"dead_letter", st.DeadLetter}}
		if st.Oversize != nil {
			roles = append(roles, struct {
				role string
				sink Sink
			}{"archive", st.Oversize.Archive})
		}
		for _, r := range roles {
			if r.sink.Type == "" {
				continue
			}
			d, err := describeOne(sinks, r.role, r.sink)
			if err != nil {
				return nil, fmt.Errorf("stream %s, %s: %w", st.Path, r.role, err)
			}
			ms.Destinations = append(ms.Destinations, d)
		}
		m.Streams = append(m.Streams, ms)
	}
	return m, nil
}

func describeOne(sinks *Sinks, role string, s Sink) (ManifestDestination, error) {
	d := ManifestDestination{Role: role, Kind: s.Type}
	target, known, err := sinks.Locate(s)
	switch {
	case errors.Is(err, ErrNoIdentity):
		d.Note = err.Error()
		return d, nil
	case err != nil:
		return d, err
	case !known:
		d.Note = "this gateway binary has no locator for " + s.Type
		return d, nil
	}
	d.Target = &target
	// auto_table is the one sink that writes tables it learns from events.
	d.Routes = s.Type == SinkAutoTable
	return d, nil
}

// describeMain is `gateway describe [--name n] [--out file] <config.yaml>`.
//
// --out exists for the deploy Job: the gateway's image has no shell, so the
// container that describes cannot redirect stdout into the file the container
// that publishes reads.
func describeMain(args []string, sinks *Sinks, out io.Writer) error {
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	name := fs.String("name", "", "the gateway's name in the manifest (default: the config's `name:`)")
	file := fs.String("out", "", "write the manifest to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: gateway describe [--name <gateway>] [--out <file>] <config.yaml>")
	}
	cfg, err := Load(fs.Arg(0))
	if err != nil {
		return err
	}
	m, err := Describe(cfg, sinks, *name, time.Now())
	if err != nil {
		return err
	}
	if *file != "" {
		// Written only once the manifest is whole: a describe that failed
		// leaves no file for the publish to store half of.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m); err != nil {
			return err
		}
		return os.WriteFile(*file, buf.Bytes(), 0o644) // #nosec G306 -- a manifest names destinations, never credentials
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}
