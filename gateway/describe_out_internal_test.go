package gateway

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The gateway's image has no shell, so a deploy Job cannot redirect stdout
// into a file for the next container: --out writes it.
func TestDescribeWritesTheManifestWhereItIsTold(t *testing.T) {
	t.Setenv("BREVIS_ORDERS_DSN", "postgres://loader:s3cret@db.internal:5432/shop")
	out := filepath.Join(t.TempDir(), "manifest.json")
	var stdout bytes.Buffer

	if err := describeMain([]string{"--out", out, "example/gateway.yaml"}, nil, &stdout); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("with --out, stdout got %d bytes", stdout.Len())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Gateway != "events_gateway" || m.Version != 2 {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
}

// Without --out, stdout, as before.
func TestDescribeWritesToStdoutByDefault(t *testing.T) {
	t.Setenv("BREVIS_ORDERS_DSN", "postgres://loader:s3cret@db.internal:5432/shop")
	var stdout bytes.Buffer
	if err := describeMain([]string{"example/gateway.yaml"}, nil, &stdout); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("stdout is not a manifest: %s", stdout.String())
	}
}
