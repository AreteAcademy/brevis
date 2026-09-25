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
			registered := exampleHooks(t)
			for _, s := range cfg.Streams {
				if s.Hook == "" {
					continue
				}
				if !registered[s.Hook] {
					t.Errorf("stream %q names hook %q, which example/main.go "+
						"does not register", s.Name, s.Hook)
				}
			}
		})
	}
}

// exampleHooks reads the names out of the example binary's source.
//
// Reading the source rather than listing them here on purpose: a list in the
// test is a second place the names live, and two places for one fact is how
// they start to disagree.
func exampleHooks(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("example/main.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		_, rest, found := strings.Cut(line, `MustRegister("`)
		if !found {
			continue
		}
		name, _, ok := strings.Cut(rest, `"`)
		if ok {
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("example/main.go registers no hook, so this test proves nothing")
	}
	return out
}
