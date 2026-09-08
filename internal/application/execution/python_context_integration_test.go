package execution_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// The acceptance criterion: THREE languages, one run, real processes.
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

	// A real Go binary, built against sdk/context, so the Go end of the contract
	// is exercised as a consumer sees it rather than as this repository's own
	// package call.
	goStep := filepath.Join(t.TempDir(), "gostep")
	build := exec.Command("go", "build", "-o", goStep, ".")
	build.Dir = filepath.Join(repo, "internal", "application", "execution", "testdata", "gostep")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the Go step: %v\n%s", err, out)
	}

	w := wf.Workflow{
		Slug: "cross-language", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			// No SDK at all: the test of the contract.
			{ID: "extract", Run: `sh -c 'printf "{\"bucket\":\"s3://landing/2026-09-07\"}" > "$BREVIS_OUTPUT"'`},
			// Go, through sdk/context.
			{ID: "check", Run: goStep},
			// Python, through lib/python-context.
			{ID: "transform", Run: python + " " + script},
			// And a fourth step reading what all three published.
			{ID: "load", Run: `sh -c 'echo "$BREVIS_INPUT" > ` + dir + `/seen'`},
		},
		Edges: []wf.Edge{
			{From: "extract", To: "check"},
			{From: "check", To: "transform"},
			{From: "transform", To: "load"},
		},
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

	// Each language read what the one before it published.
	for _, want := range []string{
		"go read: s3://landing/2026-09-07",     // Go read bash
		"python read: s3://landing/2026-09-07", // Python read bash, through Go
	} {
		var read bool
		for _, e := range rep.eventos {
			if strings.Contains(e.Message, want) {
				read = true
			}
		}
		if !read {
			t.Errorf("no step reported %q. Events: %+v", want, rep.eventos)
		}
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
	// What Go published survived a Python hop, and what bash published survived
	// both. Three implementations of one wire format, and the transitive read
	// crossing every one of them.
	if seen["check"]["checked_by"] != "go" || seen["check"]["rows"] != float64(7) {
		t.Errorf("what the Go step published did not survive: %v", seen["check"])
	}
	if seen["extract"]["bucket"] != "s3://landing/2026-09-07" {
		t.Errorf("the transitive context did not survive a Go and a Python hop: %v", seen)
	}
}

// TestThePythonLibraryNamesEveryVariableTheRunnerInjects.
//
// The runner injects eleven variables and the Python library is one of the two
// ways a step reads them. Nothing compiled both, so a variable added here
// reached the pods and stayed invisible to every Python step until somebody
// happened to read the runner's source.
//
// That is not hypothetical: BREVIS_MAP_VALUE shipped with `for_each:` and the
// library never named it, so every mapped Python step read os.environ by hand
// -- and the ones that did not know the engine LEAVES IT OUT on an unmapped
// step read "" and carried on.
//
// So the list is asserted. A variable the library deliberately does not expose
// goes in `internal` below, with the reason; anything else is a gap.
func TestThePythonLibraryNamesEveryVariableTheRunnerInjects(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}

	// Read the runner's OWN source rather than a list typed here: a list typed
	// here is the second place that goes stale, which is the bug this exists
	// against.
	runner, err := os.ReadFile(filepath.Join(repo, "internal", "application",
		"execution", "runner.go"))
	if err != nil {
		t.Fatal(err)
	}
	var lib strings.Builder
	for _, name := range []string{"run.py", "context.py"} {
		b, err := os.ReadFile(filepath.Join(repo, "lib", "python-context", "src",
			"brevis", name))
		if err != nil {
			t.Fatal(err)
		}
		lib.Write(b)
	}

	// Variables the library deliberately does not name, and why.
	internal := map[string]string{
		// Set by the SDK's own config, not by the runner's step contract.
		"BREVIS_SDK_": "SDK configuration, not this execution",
	}

	injected := regexp.MustCompile(`"(BREVIS_[A-Z_]+)"`).FindAllStringSubmatch(string(runner), -1)
	seen := map[string]bool{}
	var missing []string
	for _, m := range injected {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		var excused bool
		for prefix := range internal {
			if strings.HasPrefix(name, prefix) {
				excused = true
			}
		}
		if excused || strings.Contains(lib.String(), name) {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Errorf("the runner injects %v, and lib/python-context names none of them.\n"+
			"A Python step can only read those with os.environ and its own parsing, "+
			"which is what that library exists to remove. Expose them in "+
			"src/brevis/run.py, or add them to `internal` here with the reason.",
			missing)
	}
	if len(seen) < 8 {
		t.Errorf("only %d variables were found in runner.go; the regexp stopped "+
			"matching and this check is no longer checking anything", len(seen))
	}
}

// TestIntegrationAPythonStepReadsTheClockAndItsElement.
//
// The unit tests on either side set environment variables by hand. This one
// does not: the RUNNER injects them, a real python3 parses them with the
// published library, and what it read comes back through the context.
//
// It is the only test that would have caught the two things worth catching --
// the engine writing a field name the library does not read, and the library
// being right about a shape the engine never actually produces.
func TestIntegrationAPythonStepReadsTheClockAndItsElement(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on this machine")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	libSrc := filepath.Join(repo, "lib", "python-context", "src")

	dir := t.TempDir()
	script := filepath.Join(dir, "step.py")
	// It writes a FILE rather than publishing context, because a mapped step
	// publishes nothing downstream -- four instances cannot share one key, and
	// that is the engine's rule, not a limitation of this test.
	if err := os.WriteFile(script, []byte(`
import json, os
from brevis import run

# The clock, the window and the element. None of it is computed here, which is
# the whole point.
with open(os.environ["READINGS"], "w") as f:
    json.dump({
        "clock": run.now().isoformat(),
        "date": run.auto().date,
        "late": run.auto().delay_seconds,
        "window": [d.isoformat() for d in run.window()],
        "partition": run.map_value(),
        "index": run.map_index(),
        "trigger": run.context().trigger,
    }, f)
`), 0o600); err != nil {
		t.Fatal(err)
	}

	exe, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}

	slot := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	w := wf.Workflow{
		Slug: "python-clock", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "discover", Run: `sh -c 'printf "{\"parts\":[\"2026-01\"]}" > "$BREVIS_OUTPUT"'`},
			{ID: "load", Run: python + " " + script, ForEach: "discover.parts"},
		},
		Edges: []wf.Edge{{From: "discover", To: "load"}},
	}

	r := app.Runner{
		Processo: exe, Report: &coletor{}, ContextDir: t.TempDir(),
		LogicalDate: &slot,
		// A run that was thirty-seven minutes late, which is the case the
		// clock exists for: reading now() here would give the wall clock.
		Auto: run.Auto(
			run.Run{LogicalDate: &slot},
			slot.Add(37*time.Minute),
			run.Previous{},
			run.Interval{Start: slot.AddDate(0, 0, -1), End: slot},
		),
		Trigger: "schedule",
		Env: map[string]string{
			"PATH": os.Getenv("PATH"), "PYTHONPATH": libSrc,
			"READINGS": filepath.Join(dir, "readings.json"),
		},
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	// readJSON is for the context file, which is a map of maps; these readings
	// are one flat object.
	raw, err := os.ReadFile(filepath.Join(dir, "readings.json"))
	if err != nil {
		t.Fatalf("the Python step wrote nothing: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	// The SLOT, and not the wall clock: this run started at 04:37.
	if got["clock"] != "2026-09-08T04:00:00+00:00" {
		t.Errorf("clock = %v, wanted the slot -- Python read the wall clock", got["clock"])
	}
	if got["date"] != "2026-09-08" {
		t.Errorf("date = %v", got["date"])
	}
	if got["late"] != float64(2220) {
		t.Errorf("late = %v, wanted 2220 seconds", got["late"])
	}
	// The window, from the cron and not from the wall clock.
	window, _ := got["window"].([]any)
	if len(window) != 2 || window[0] != "2026-09-07T04:00:00+00:00" ||
		window[1] != "2026-09-08T04:00:00+00:00" {
		t.Errorf("window = %v", got["window"])
	}
	// And the element, without its quotes, which is the engine's own rule.
	if got["partition"] != "2026-01" || got["index"] != float64(0) {
		t.Errorf("the mapped instance read %v / %v", got["partition"], got["index"])
	}
	if got["trigger"] != "schedule" {
		t.Errorf("trigger = %v", got["trigger"])
	}
}
