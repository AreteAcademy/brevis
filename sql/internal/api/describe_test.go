package api

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/connections"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func describeFixture(t *testing.T) []byte {
	t.Helper()
	t.Setenv("DESCRIBE_DSN", "postgres://reader:secret@db:5432/analytics?sslmode=disable")
	reg := filepath.Join(t.TempDir(), "brevis.yaml")
	if err := os.WriteFile(reg, []byte("connections:\n  - {name: warehouse, dialect: postgres, dsn_from: DESCRIBE_DSN}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := connections.Load(reg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "describe", "api.yaml")
	cfg, err := Load(path, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Describe(path, cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// THE MANIFEST IS THE FILE AS IT RUNS: every endpoint with its parameters
// resolved through their refs, its effective limits, the relations its SQL
// reads as catalog targets, and the `#:` notes its author marked. A golden
// file, so a change to any of it is a diff somebody reads.
func TestTheManifestIsTheFileAsItRuns(t *testing.T) {
	got := describeFixture(t)
	golden := filepath.Join("testdata", "describe", "manifest.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the manifest changed:\n%s", got)
	}
}

// WHAT NEVER LEAVES: the SQL text, an ordinary comment, the DSN, the
// variable values. The SQL is in the customer's repository; the console
// shows which models it reads, which is what an operator acts on.
func TestTheManifestCarriesNoSQLNoSecretAndNoUnmarkedComment(t *testing.T) {
	got := string(describeFixture(t))
	for _, never := range []string{"select", "{{", "secret", "reader", "ordinary comment", "CACHE_ADDR="} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(never)) {
			t.Errorf("the manifest holds %q:\n%s", never, got)
		}
	}
	for _, must := range []string{`"postgres://analytics/marts/clicks_daily"`, "Clicks per day", "as the ad platform spells it", `"max_rows": 500`} {
		if !strings.Contains(got, must) {
			t.Errorf("the manifest lacks %q", must)
		}
	}
}
