package gateway_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
)

// The shipped examples have to load.
//
// They are what somebody copies, and a reference config the product refuses is
// worse than none: it teaches a shape that does not work and costs the reader
// the time to find out. Nothing here asserted it until a `write:` field was
// added to the file and nothing noticed either way.
func TestTheShippedExamplesLoad(t *testing.T) {
	// BREVIS_ENV decides whether an open endpoint is allowed, and the two
	// files differ on exactly that: gateway.yaml declares auth, the local one
	// deliberately has none.
	t.Setenv("BREVIS_ENV", gateway.EnvLocal)

	files, err := filepath.Glob("example/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no example config was found, and this test is why they exist")
	}

	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			cfg, err := gateway.Load(f)
			if err != nil {
				t.Fatalf("the example does not load: %v", err)
			}
			if len(cfg.Streams) == 0 {
				t.Fatal("the example declares no stream")
			}

			// Every hook the file names has to be one the example binary
			// registers, or the file documents a gateway that will not start.
			hooks := registered(t, `MustRegister("`)
			for _, s := range cfg.Streams {
				if s.Hook == "" {
					continue
				}
				if !hooks[s.Hook] {
					t.Errorf("stream %q names hook %q, which example/main.go "+
						"does not register", s.Name, s.Hook)
				}
			}

			// And every sink, which is the same rule and a newer way to get it
			// wrong: sinks are compiled in now, so a stream naming one the
			// example binary did not import is a file documenting a gateway
			// that refuses to start. The refusal is good; shipping the file
			// that triggers it is not.
			sinks := registered(t, `MustRegister(`)
			for _, s := range cfg.Streams {
				for _, use := range []struct{ what, typ string }{
					{"sink", s.Sink.Type},
					{"dead_letter", s.DeadLetter.Type},
				} {
					if use.typ == "" || sinks[use.typ] {
						continue
					}
					t.Errorf("stream %q's %s is %q, which example/main.go does "+
						"not register", s.Name, use.what, use.typ)
				}
			}
		})
	}
}

// registered reads what the example binary registers out of its own source.
//
// Reading the source rather than listing the names here on purpose: a list in
// the test is a second place the names live, and two places for one fact is how
// they start to disagree.
//
// Two forms, because the two registries are written differently. A hook is a
// string literal -- MustRegister("enrich_clicks", …) -- and a sink is the
// driver package's own constant -- MustRegister(pubsub.Sink, …) -- which is
// itself the right call: a YAML type name spelled twice is a typo waiting to
// happen. So a sink is read as `pubsub.Sink` and the package name IS the type
// name, which holds because every driver here names its constant `Sink`.
func registered(t *testing.T, marker string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("example/main.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		_, rest, found := strings.Cut(line, marker)
		if !found {
			continue
		}
		if marker == `MustRegister("` {
			if name, _, ok := strings.Cut(rest, `"`); ok {
				out[name] = true
			}
			continue
		}
		// `pubsub.Sink, pubsub.New)` -> pubsub
		if pkg, after, ok := strings.Cut(rest, "."); ok && strings.HasPrefix(after, "Sink,") {
			out[pkg] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("example/main.go registers nothing matching %q, so this test "+
			"proves nothing", marker)
	}
	return out
}
