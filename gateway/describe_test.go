package gateway_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

var update = flag.Bool("update", false, "rewrite the describe golden files")

// describe over every example config, compared with a golden file. The
// manifest is a contract the engine reads; a change to it shows up here as a
// diff somebody has to look at.
func TestDescribeMatchesTheGoldenManifestForEveryExample(t *testing.T) {
	t.Setenv("BREVIS_ORDERS_DSN", "postgres://loader:s3cret@db.internal:5432/shop")
	// The local example's `orders` stream, which lands in the warehouse
	// `make up-data` brings up. A DSN here and not a real one: `describe`
	// parses it and never dials, and the golden file below is where anybody
	// can check that no password reached the manifest.
	t.Setenv("WAREHOUSE_DSN", "postgres://brevis:brevis@warehouse:5432/warehouse?sslmode=disable")
	at := time.Date(2026, 10, 7, 18, 2, 11, 0, time.UTC)

	for _, name := range []string{"gateway.yaml", "gateway.local.yaml"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := gateway.Load(filepath.Join("example", name))
			if err != nil {
				t.Fatal(err)
			}
			m, err := gateway.Describe(cfg, locators(), "", at)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(m, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", "describe", name+".golden.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("describe differs from %s:\n%s", golden, got)
			}
		})
	}
}

func TestDescribeNamesTheGatewayByItsConfigUnlessToldOtherwise(t *testing.T) {
	cfg := &gateway.Config{Name: "events_gateway"}
	m, err := gateway.Describe(cfg, locators(), "", time.Now())
	if err != nil || m.Gateway != "events_gateway" {
		t.Fatalf("gateway = %q, %v", m.Gateway, err)
	}
	m, _ = gateway.Describe(cfg, locators(), "edge", time.Now())
	if m.Gateway != "edge" {
		t.Fatalf("--name was not used: %q", m.Gateway)
	}
}

// A relative local path is relative to the gateway's working directory, which
// describe -- running in CI -- cannot see. It is listed with no target and a
// reason, never with an absolute path that belongs to the CI machine.
func TestARelativePathHasNoIdentityAndSaysWhy(t *testing.T) {
	cfg := &gateway.Config{Name: "g", Streams: []gateway.Stream{{
		Path: "/v1/x",
		Sink: gateway.Sink{Type: "files", Path: "./out/"},
	}}}
	m, err := gateway.Describe(cfg, locators(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := m.Streams[0].Destinations[0]
	if d.Target != nil || d.Note == "" {
		t.Fatalf("destination = %+v, want no target and a note", d)
	}
}

// A sink this binary has no locator for is listed, unidentified, rather than
// left out: the console must not show fewer destinations than exist.
func TestASinkWithoutALocatorIsListedUnidentified(t *testing.T) {
	cfg := &gateway.Config{Name: "g", Streams: []gateway.Stream{{
		Path: "/v1/x", Sink: gateway.Sink{Type: "kafka"},
	}}}
	m, err := gateway.Describe(cfg, locators(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d := m.Streams[0].Destinations[0]; d.Target != nil || d.Kind != "kafka" || d.Role != "sink" {
		t.Fatalf("destination = %+v", d)
	}
}
