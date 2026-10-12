package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func example(t *testing.T, name string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "examples", "endpoints", name)
}

// THE EXAMPLE IS A TEST. A file in examples/ that no longer loads is a
// quickstart that fails on its first command.
func TestTheExampleValidates(t *testing.T) {
	t.Setenv("WAREHOUSE_DSN", "postgres://u:p@localhost:5432/w?sslmode=disable")
	var out, errOut bytes.Buffer
	code := run([]string{"validate", "--config", example(t, "api.yaml"),
		"--connections", example(t, "brevis.yaml")}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "2 endpoint") {
		t.Errorf("validate said %q", out.String())
	}
}

func TestAFileThatCannotBeHonouredExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"validate", "--config", filepath.Join(t.TempDir(), "absent.yaml")}, &out, &errOut)
	if code != 1 || errOut.Len() == 0 {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestAnUnknownCommandIsRefused(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errOut); code != 2 {
		t.Errorf("exit %d", code)
	}
}

// THE KEY ARRIVES ON STDIN, never as an argument: an argument is in the
// shell's history and in every `ps` while it runs.
func TestKeyHashReadsTheKeyFromStdinAndPrintsItsLine(t *testing.T) {
	var out, errOut bytes.Buffer
	stdin = strings.NewReader("a-key-long-enough-0123456789abcdef\n")
	defer func() { stdin = nil }()
	code := run([]string{"key", "hash", "--tenant", "acme"}, &out, &errOut)
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
	dir := filepath.Join(filepath.Dir(here), "..", "..", "internal", "api", "testdata")
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
		code := run([]string{"validate", "--config", f, "--connections", filepath.Join(dir, "brevis.yaml")}, &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), says) {
			t.Errorf("%s: exit %d, said %q; wanted 1 and %q", filepath.Base(f), code, errOut.String(), says)
		}
	}
}
