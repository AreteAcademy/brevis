package execution_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// The acceptance criterion: two languages, one run, real processes.
//
// A Python step importing lib/python-context reads what a bash step published
// with nothing but `printf`, and publishes something a third step reads with
// `jq`-less shell. If this passes, the contract is genuinely language-neutral;
// if it needed the Go SDK on both ends, the design would be "works if you use
// Go" and the community this is for could not use it.
//
// It skips without a python3, and that is the honest trade: it must not turn
// the suite red on a machine that has no Python.
func TestIntegrationPythonAndShellExchangeContext(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on this machine")
	}

	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	libSrc := filepath.Join(repo, "lib", "python-context", "src")
	if _, err := os.Stat(filepath.Join(libSrc, "brevis", "context.py")); err != nil {
		t.Fatalf("the library is not where this test expects it: %v", err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "step.py")
	// The whole consumer-facing surface, in the shape the request asked for.
	if err := os.WriteFile(script, []byte(`
from brevis import context

bucket = context.get("extract.bucket")
print("python read:", bucket)

context.set(name="Daniel")
context.set(label="Nome")
context.set(rows=48213)
`), 0o600); err != nil {
		t.Fatal(err)
	}

	exe, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}

	w := wf.Workflow{
		Slug: "cross-language", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			// No SDK at all: the test of the contract.
			{ID: "extract", Run: `sh -c 'printf "{\"bucket\":\"s3://landing/2026-09-07\"}" > "$BREVIS_OUTPUT"'`},
			// Python, through the library.
			{ID: "transform", Run: python + " " + script},
			// And a third step reading what Python published.
			{ID: "load", Run: `sh -c 'echo "$BREVIS_INPUT" > ` + dir + `/seen'`},
		},
		Edges: []wf.Edge{{From: "extract", To: "transform"}, {From: "transform", To: "load"}},
	}

	rep := &coletor{}
	r := app.Runner{
		Processo: exe, Report: rep, ContextDir: t.TempDir(),
		Env: map[string]string{
			"PATH": os.Getenv("PATH"),
			// The library is not installed; this is how a checkout runs it.
			"PYTHONPATH": libSrc,
		},
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("the cross-language pipeline failed: %v", err)
	}

	// Python read what bash published.
	var read bool
	for _, e := range rep.eventos {
		if strings.Contains(e.Message, "python read: s3://landing/2026-09-07") {
			read = true
		}
	}
	if !read {
		t.Errorf("the Python step did not receive the shell step's context. Events: %+v",
			rep.eventos)
	}

	// And the step after read what Python published -- including both keys of
	// the two separate set() calls, which is the merge the request asked about.
	seen := readJSON(t, filepath.Join(dir, "seen"))
	got := seen["transform"]
	if got["name"] != "Daniel" || got["label"] != "Nome" {
		t.Errorf("the two set() calls did not both survive: %v", got)
	}
	if got["rows"] != float64(48213) {
		t.Errorf("rows = %v, want 48213", got["rows"])
	}
	// The shell step is still visible transitively, through Python.
	if seen["extract"]["bucket"] != "s3://landing/2026-09-07" {
		t.Errorf("the transitive context did not survive a Python hop: %v", seen)
	}
}
