package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookedFile writes an endpoint file whose one endpoint names a before and
// an after, and the registry beside it.
func hookedFile(t *testing.T) (config, registry string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APP_DSN", "postgres://reader:pw@db:5432/app?sslmode=disable")
	registry = filepath.Join(dir, "brevis.yaml")
	if err := os.WriteFile(registry, []byte(`
connections:
  - name: warehouse
    dialect: postgres
    dsn_from: APP_DSN
`), 0o600); err != nil {
		t.Fatal(err)
	}
	config = filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(config, []byte(`
name: metrics-api
connection: warehouse
endpoints:
  - name: orders
    path: /v1/orders
    tenant: none
    sql: select 1 as k
    hook: {before: check_window, after: to_series}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, registry
}

// A BINARY OF YOUR OWN is the published one plus a registry: the same
// command, reading the same file, with the names it uses defined.
func TestABinaryWithHooksValidatesAFileThatUsesThem(t *testing.T) {
	config, registry := hookedFile(t)
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", func(context.Context, *Request) error { return nil })
	hooks.MustRegisterAfter("to_series", func(_ context.Context, r *Result) (any, error) { return r.Rows, nil })
	var out, errOut bytes.Buffer
	if code := run([]string{"validate", "--config", config, "--connections", registry}, hooks, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

// THE PUBLISHED BINARY REGISTERS NONE, and a file with `hook:` is refused
// before anything listens -- saying where a binary that has them comes from.
func TestThePublishedBinaryRefusesAFileWithHooks(t *testing.T) {
	config, registry := hookedFile(t)
	var out, errOut bytes.Buffer
	code := run([]string{"validate", "--config", config, "--connections", registry}, NewHooks(), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "sql/example") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}

// Refuse is the internal one: a before written against this package answers
// the caller exactly as the server expects.
func TestRefuseIsTheServersRefusal(t *testing.T) {
	err := Refuse("the window is wider than 90 days")
	if err == nil || err.Error() != "the window is wider than 90 days" {
		t.Fatalf("Refuse gave %v", err)
	}
}
