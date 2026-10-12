package main

import (
	"bytes"
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
	if !strings.Contains(out.String(), "1 endpoint") {
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
