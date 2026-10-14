package api

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	api "github.com/AreteAcademy/brevis/sql/internal/api"
)

func example(t *testing.T, name string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "examples", "endpoints", name)
}

// THE EXAMPLE IS A TEST. A file in examples/ that no longer loads is a
// quickstart that fails on its first command.
func TestTheExampleValidates(t *testing.T) {
	t.Setenv("WAREHOUSE_DSN", "postgres://u:p@localhost:5432/w?sslmode=disable")
	var out, errOut bytes.Buffer
	code := run([]string{"validate", "--config", example(t, "api.yaml"),
		"--connections", example(t, "brevis.yaml")}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "2 endpoint") {
		t.Errorf("validate said %q", out.String())
	}
}

func TestAFileThatCannotBeHonouredExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"validate", "--config", filepath.Join(t.TempDir(), "absent.yaml")}, nil, &out, &errOut)
	if code != 1 || errOut.Len() == 0 {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestAnUnknownCommandIsRefused(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"frobnicate"}, nil, &out, &errOut); code != 2 {
		t.Errorf("exit %d", code)
	}
}

// THE KEY ARRIVES ON STDIN, never as an argument: an argument is in the
// shell's history and in every `ps` while it runs.
func TestKeyHashReadsTheKeyFromStdinAndPrintsItsLine(t *testing.T) {
	var out, errOut bytes.Buffer
	stdin = strings.NewReader("a-key-long-enough-0123456789abcdef\n")
	defer func() { stdin = nil }()
	code := run([]string{"key", "hash", "--tenant", "acme"}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if line := strings.TrimSpace(out.String()); !strings.HasSuffix(line, "=acme") || len(line) != 64+len("=acme") {
		t.Errorf("printed %q, wanted 64 hex characters and =acme", line)
	}
	if strings.Contains(out.String(), "a-key-long") {
		t.Error("the key was printed")
	}
}

// EVERY FIXTURE IN testdata/refused EXITS 1 WITH ITS MESSAGE. Each file's
// first line says what the refusal must mention, so a new refusal is a new
// file and not a new test.
func TestEveryRefusedFixtureExitsOneWithItsMessage(t *testing.T) {
	t.Setenv("REFUSED_FIXTURE_DSN", "postgres://u:p@localhost:5432/w?sslmode=disable")
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(here), "..", "internal", "api", "testdata")
	files, err := filepath.Glob(filepath.Join(dir, "refused", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(raw), "\n")
		says, marked := strings.CutPrefix(first, "# refused: ")
		if !marked {
			t.Errorf("%s: the first line is not `# refused: <what the error says>`", filepath.Base(f))
			continue
		}
		var out, errOut bytes.Buffer
		code := run([]string{"validate", "--config", f, "--connections", filepath.Join(dir, "brevis.yaml")}, nil, &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), says) {
			t.Errorf("%s: exit %d, said %q; wanted 1 and %q", filepath.Base(f), code, errOut.String(), says)
		}
	}
}

// --out WRITES THE MANIFEST ONCE IT IS WHOLE, for a publish Job whose image
// has no shell to redirect into a file -- the gateway's flag, for its reason.
func TestDescribeWritesTheManifestToOut(t *testing.T) {
	t.Setenv("WAREHOUSE_DSN", "postgres://u:p@localhost:5432/w?sslmode=disable")
	out := filepath.Join(t.TempDir(), "manifest.json")
	var stdout, errOut bytes.Buffer
	code := run([]string{"describe", "--config", example(t, "api.yaml"),
		"--connections", example(t, "brevis.yaml"), "--out", out}, nil, &stdout, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(raw), `"postgres://w/marts/orders_daily"`) {
		t.Errorf("manifest %s, %v", raw, err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout got %q with --out", stdout.String())
	}
}

// --addr AND --metrics-addr OVERRIDE THE FILE, for a container: the file
// says 127.0.0.1 for a laptop, and inside a container that is an address
// nothing outside it can reach. One file for both, rather than a second copy
// that differs in one line and drifts in the others.
func TestTheListenFlagsOverrideTheFile(t *testing.T) {
	cfg := &api.Config{Listen: api.Listen{Addr: "127.0.0.1:8095"}}
	cfg.Metrics.Addr = "127.0.0.1:9197"
	overrideListen(cfg, ":8080", ":9090")
	if cfg.Listen.Addr != ":8080" || cfg.Metrics.Addr != ":9090" {
		t.Errorf("listen %q metrics %q", cfg.Listen.Addr, cfg.Metrics.Addr)
	}
	overrideListen(cfg, "", "")
	if cfg.Listen.Addr != ":8080" || cfg.Metrics.Addr != ":9090" {
		t.Error("empty flags changed the addresses")
	}
}
