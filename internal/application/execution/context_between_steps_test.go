package execution_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// The pipeline the request described, with real processes.
//
// extract publishes; transform reads it and publishes its own; load reads both
// -- extract transitively, which is the case that makes `depends_on` the right
// scope and a second `needs:` field unnecessary.
func TestContextTravelsAcrossSteps(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: `sh -c 'printf "{\"bucket\":\"s3://landing\"}" > "$BREVIS_OUTPUT"'`},
			{ID: "transform", Run: `sh -c 'echo "$BREVIS_INPUT" > ` + dir + `/seen-transform; printf "{\"rows\":42}" > "$BREVIS_OUTPUT"'`},
			{ID: "load", Run: `sh -c 'echo "$BREVIS_INPUT" > ` + dir + `/seen-load'`},
		},
		Edges: []wf.Edge{{From: "extract", To: "transform"}, {From: "transform", To: "load"}},
	}

	r := app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("the pipeline failed: %v", err)
	}

	// transform sees only extract.
	seen := readJSON(t, filepath.Join(dir, "seen-transform"))
	if seen["extract"]["bucket"] != "s3://landing" {
		t.Errorf("transform did not receive extract's context: %v", seen)
	}
	// Note this file does NOT assert isolation here: in a straight chain, a
	// step below has published nothing yet when the one above reads, so the
	// assertion would pass with the scoping removed entirely. It gets its own
	// fixture below, where it can actually fail.

	// load sees transform AND extract, the second one transitively.
	seen = readJSON(t, filepath.Join(dir, "seen-load"))
	if seen["transform"]["rows"] != float64(42) {
		t.Errorf("load did not receive transform's context: %v", seen)
	}
	if seen["extract"]["bucket"] != "s3://landing" {
		t.Errorf("load lost the transitive context, so every step would have to "+
			"copy its input forward: %v", seen)
	}
}

// A step cannot read a step it does not depend on, and this graph is what makes
// that assertion able to fail:
//
//	a ──> b ──> d
//	 └──> c
//
// `c` and `b` run together, `d` runs after both, and `d` depends only on `b`.
// So when `d` reads, `c` HAS published -- and `d` must still not see it. In a
// straight chain the same assertion passes with the scoping deleted, which is
// why the fixture has a fork in it.
func TestAStepCannotReadASiblingItDoesNotDependOn(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	w := wf.Workflow{
		Slug: "fork", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "a", Run: `sh -c 'printf "{\"from\":\"a\"}" > "$BREVIS_OUTPUT"'`},
			{ID: "b", Run: `sh -c 'printf "{\"from\":\"b\"}" > "$BREVIS_OUTPUT"'`},
			{ID: "c", Run: `sh -c 'printf "{\"from\":\"c\"}" > "$BREVIS_OUTPUT"'`},
			{ID: "d", Run: `sh -c 'echo "$BREVIS_INPUT" > ` + dir + `/seen-d'`},
		},
		Edges: []wf.Edge{
			{From: "a", To: "b"}, {From: "a", To: "c"}, {From: "b", To: "d"},
		},
	}

	r := app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	seen := readJSON(t, filepath.Join(dir, "seen-d"))
	if _, leaked := seen["c"]; leaked {
		t.Errorf("d read a step it does not depend on: %v. Isolation is what "+
			"lets two steps publish the same key without either losing it", seen)
	}
	if seen["b"]["from"] != "b" || seen["a"]["from"] != "a" {
		t.Errorf("d lost what it does depend on: %v", seen)
	}
}

// A step that publishes nothing changes nothing.
//
// Most steps publish nothing, and BREVIS_INPUT must not appear holding `{}` --
// a consumer reading that learns the step ran and published an empty object.
func TestAStepThatPublishesNothingLeavesTheNextOneClean(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	w := wf.Workflow{
		Slug: "quiet", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "a", Run: "echo hello"},
			{ID: "b", Run: `sh -c 'echo "[${BREVIS_INPUT}]" > ` + dir + `/seen'`},
		},
		Edges: []wf.Edge{{From: "a", To: "b"}},
	}

	r := app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "seen"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != "[]\n" {
		t.Errorf("BREVIS_INPUT = %q, want it absent entirely", got)
	}
}

// A truncated payload is reported, not dropped.
//
// The platform CUTS the termination message rather than refusing it, so this is
// what actually arrives. Dropping it hands the next step a missing key with
// nothing anywhere saying why.
func TestATruncatedPayloadIsReportedOnTheStepsLog(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}

	// A JSON object cut mid-string, exactly as a truncation leaves it.
	cut := `{"bucket":"s3://landing/` + repeat("x", runcontext.MaxBytes)
	w := wf.Workflow{
		Slug: "cut", Kind: wf.KindDAG,
		Nodes: []wf.Node{{ID: "a", Run: `sh -c 'printf '"'"'` + cut + `'"'"' > "$BREVIS_OUTPUT"'`}},
	}

	rep := &coletor{}
	r := app.Runner{
		Processo: exec, Report: rep, ContextDir: t.TempDir(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("a bad payload must not fail the step: %v", err)
	}

	var said bool
	for _, e := range rep.eventos {
		if contains(e.Message, "not usable") && contains(e.Message, "truncat") {
			said = true
		}
	}
	if !said {
		t.Errorf("the truncation was swallowed; the step below would get a "+
			"missing key with nothing saying why. Events: %+v", rep.eventos)
	}
}

func readJSON(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := map[string]map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s is not the expected JSON (%q): %v", path, raw, err)
	}
	return out
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, s...)
	}
	return string(out)
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
