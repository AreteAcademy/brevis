package gateway_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/sink/autotable"
	"github.com/AreteAcademy/brevis/gateway/store/gcs"
	"github.com/AreteAcademy/brevis/gateway/store/s3"
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

			// And every hook an oversize block names, which is a second
			// registry entry the same file has to carry.
			for _, st := range cfg.Streams {
				if st.Oversize == nil || st.Oversize.Hook == "" {
					continue
				}
				if !hooks[st.Oversize.Hook] {
					t.Errorf("stream %q's oversize.hook is %q, which "+
						"example/main.go does not register", st.Name, st.Oversize.Hook)
				}
			}

			// And every object store a path implies. This is the half that
			// sink types do not cover: `gs://` and `s3://` are both the
			// `files` sink, and which BACKEND the binary carries is a
			// different registration. An example naming s3:// while its main
			// registers only GCS is a file documenting a gateway that refuses
			// to start.
			for _, st := range cfg.Streams {
				paths := []struct{ what, path string }{
					{"sink", st.Sink.Path},
					{"dead_letter", st.DeadLetter.Path},
				}
				if st.Oversize != nil {
					paths = append(paths, struct{ what, path string }{
						"oversize.archive", st.Oversize.Archive.Path})
				}
				for _, p := range paths {
					pkg, needed := storeFor(p.path)
					if !needed || strings.Contains(source(t), pkg) {
						continue
					}
					t.Errorf("stream %q's %s is %q, and example/main.go does "+
						"not import %s", st.Name, p.what, p.path, pkg)
				}
			}

			// And every sink, which is the same rule and a newer way to get it
			// wrong: sinks are compiled in now, so a stream naming one the
			// example binary did not import is a file documenting a gateway
			// that refuses to start. The refusal is good; shipping the file
			// that triggers it is not.
			sinks := registered(t, `MustRegister(`)
			for _, s := range cfg.Streams {
				uses := []struct{ what, typ string }{
					{"sink", s.Sink.Type},
					{"dead_letter", s.DeadLetter.Type},
				}
				if s.Oversize != nil {
					// The archive is a sink like any other, and it was the one
					// this test did not look at until a stream grew one.
					uses = append(uses, struct{ what, typ string }{
						"oversize.archive", s.Oversize.Archive.Type})
				}
				if s.Sink.Into != nil {
					// And the one a ROUTER routes into, which is a third place
					// a sink type hides -- `auto_table` names its destination
					// in a nested block, and a binary that did not compile it
					// in refuses at startup.
					uses = append(uses, struct{ what, typ string }{
						"sink.into", s.Sink.Into.Type})
				}
				for _, use := range uses {
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

// sinkTypes maps a driver package to the YAML type its own Sink constant
// holds. Only the packages whose two names differ need an entry; the constant
// stays the single source of truth and this records which package it lives in.
var sinkTypes = map[string]string{
	"autotable": autotable.Sink,
}

// storeFor says which package a path needs, keyed by the schemes the store
// packages themselves declare. The constant stays in one place; only the
// import path is written here, and it is written right beside the one it
// checks for.
func storeFor(path string) (pkg string, needed bool) {
	switch {
	case strings.HasPrefix(path, s3.Scheme+"://"):
		return "gateway/store/s3", true
	case strings.HasPrefix(path, gcs.Scheme+"://"):
		return "gateway/store/gcs", true
	}
	return "", false
}

// source is example/main.go, read once per call. Small enough that caching it
// would be the more complicated thing.
func source(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("example/main.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
		// `pubsub.Sink, pubsub.New)` -> the type that package's constant holds.
		//
		// Through the constant and not the package name: they agree for five
		// of the six drivers and NOT for autotable, whose type is
		// `auto_table`. Deriving it from the package read `autotable`, found
		// no stream using that, and reported the example unregistered when it
		// was registered -- a false alarm is how a gate stops being read.
		if pkg, after, ok := strings.Cut(rest, "."); ok && strings.HasPrefix(after, "Sink,") {
			if typ, known := sinkTypes[pkg]; known {
				out[typ] = true
				continue
			}
			out[pkg] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("example/main.go registers nothing matching %q, so this test "+
			"proves nothing", marker)
	}
	return out
}
