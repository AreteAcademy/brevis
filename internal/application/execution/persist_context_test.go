package execution_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

// A step that declares keys is pointed at the store; one that declares nothing
// is not pointed anywhere.
//
// It is the whole enforcement: the SDK refuses a call when the variables are
// absent, so a missing `persist_context:` has to read as a missing declaration
// rather than as an empty key.
func TestOnlyADeclaringStepIsPointedAtTheStore(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := t.TempDir()

	// Each step writes its own environment out, so the test reads what the pod
	// would have seen rather than what the runner meant to set.
	dump := func(name string) string {
		return `sh -c 'printf "%s|%s" "$BREVIS_PERSIST_URL" "$BREVIS_PERSIST_KEYS" > ` +
			filepath.Join(dir, name) + `'`
	}

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "writer", Run: dump("writer"), PersistContext: []string{"ana.station_codes"}},
			{ID: "bystander", Run: dump("bystander")},
		},
		Edges: []wf.Edge{{From: "writer", To: "bystander"}},
	}

	if err := (app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: &spyPersister{}, RunID: uuid.New(),
		PersistURL: store,
		Env:        map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	got := read(t, filepath.Join(dir, "writer"))
	if got != store+"|ana.station_codes" {
		t.Errorf("the declaring step saw %q, want %q", got, store+"|ana.station_codes")
	}

	// The step next to it declared nothing, so it gets nothing -- not the URL
	// with an empty key list, which would let it read every key in the store.
	if got := read(t, filepath.Join(dir, "bystander")); got != "|" {
		t.Errorf("a step that declared nothing saw %q", got)
	}
}

// Several keys travel as one list, and the SDK splits it. A step declaring two
// must see both, or the second reads as undeclared.
func TestEveryDeclaredKeyIsPassedOn(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{{
			ID:             "step",
			Run:            `sh -c 'printf "%s" "$BREVIS_PERSIST_KEYS" > ` + filepath.Join(dir, "keys") + `'`,
			PersistContext: []string{"a.one", "b.two"},
		}},
	}

	if err := (app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: &spyPersister{}, RunID: uuid.New(),
		PersistURL: t.TempDir(),
		Env:        map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(dir, "keys")); got != "a.one,b.two" {
		t.Errorf("the step saw %q", got)
	}
}

// With nowhere to keep it, the run is refused BEFORE the first step.
//
// The alternative is the failure that looks like success: every read comes back
// absent, the pipeline builds an empty batch query, and the run finishes green
// having fetched nothing.
func TestAWorkflowThatNeedsAStoreIsRefusedWithoutOne(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "ran")

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{{
			ID: "step", Run: `sh -c 'touch ` + ran + `'`,
			PersistContext: []string{"ana.station_codes"},
		}},
	}

	err = app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: &spyPersister{}, RunID: uuid.New(),
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)

	if err == nil {
		t.Fatal("the run was allowed with no store configured")
	}
	for _, want := range []string{"BREVIS_PERSIST_URL", "ana.station_codes", "step"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the step ran before the refusal")
	}
}

// The 4 KB context between steps is a separate mechanism and must keep working
// untouched -- including for a step that also declares persisted context.
func TestTheEphemeralContextIsUnaffected(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	w := wf.Workflow{
		Slug: "pipeline", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{
				ID:             "extract",
				Run:            `sh -c 'printf "{\"bucket\":\"s3://landing\"}" > "$BREVIS_OUTPUT"'`,
				PersistContext: []string{"ana.station_codes"},
			},
			{
				ID:  "load",
				Run: `sh -c 'printf "%s" "$BREVIS_INPUT" > ` + filepath.Join(dir, "in") + `'`,
			},
		},
		Edges: []wf.Edge{{From: "extract", To: "load"}},
	}

	if err := (app.Runner{
		Processo: exec, Report: &coletor{}, ContextDir: t.TempDir(),
		Persist: &spyPersister{}, RunID: uuid.New(),
		PersistURL: t.TempDir(),
		Env:        map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	var in map[string]map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "in"))), &in); err != nil {
		t.Fatalf("BREVIS_INPUT is not what it was: %v", err)
	}
	if got := in["extract"]["bucket"]; got != "s3://landing" {
		t.Errorf("the ephemeral context carried %v", got)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}
