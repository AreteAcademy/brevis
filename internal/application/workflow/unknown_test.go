package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnUnknownFieldIsNamedWithItsLine(t *testing.T) {
	// `hosts` is the typo that prompted this: `host:` is the field, and the
	// plural is what a hand reaches for.
	got := UnknownFields("a.yaml", []byte(
		"name: a\ntype: dag\nsteps:\n  - id: one\n    run: echo\n    hosts: box\n"))

	if len(got) != 1 {
		t.Fatalf("got %d warnings, wanted 1: %v", len(got), got)
	}
	for _, want := range []string{"a.yaml", "line 6", "hosts", "a step", "ignored"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the warning does not say %q: %s", want, got[0])
		}
	}
	// The Go type is not the reader's business.
	if strings.Contains(got[0], "StepSpec") {
		t.Errorf("the warning leaks a Go type: %s", got[0])
	}
}

func TestEachLevelSaysWhichLevelItIs(t *testing.T) {
	for _, c := range []struct{ name, yaml, says string }{
		{"workflow", "name: a\ntype: dag\nhosts: box\nsteps:\n  - id: one\n    run: echo\n", "a workflow"},
		{"step", "name: a\ntype: dag\nsteps:\n  - id: one\n    run: echo\n    hosts: box\n", "a step"},
		{"param", "name: a\ntype: dag\nparams:\n  - name: p\n    tipo: string\nsteps:\n  - id: one\n    run: echo\n", "a param"},
		{"resources", "name: a\ntype: dag\nsteps:\n  - id: one\n    run: echo\n    resources: {memoria: 2}\n", "resources"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := UnknownFields("a.yaml", []byte(c.yaml))
			if len(got) != 1 {
				t.Fatalf("got %d warnings, wanted 1: %v", len(got), got)
			}
			if !strings.Contains(got[0], c.says) {
				t.Errorf("wanted %q in: %s", c.says, got[0])
			}
		})
	}
}

// A type added to this package later and not added to levelOf still produces a
// sentence, and still does not print the Go type at the author.
func TestATypeNobodyMappedStillReadsLikeASentence(t *testing.T) {
	got := levelOf("workflow.SomethingAddedLater")
	if got == "" || strings.Contains(got, "workflow.") {
		t.Fatalf("levelOf fell through to %q", got)
	}
}

func TestEveryUnknownFieldIsReportedAndNotJustTheFirst(t *testing.T) {
	got := UnknownFields("a.yaml", []byte(
		"name: a\ntype: dag\nsteps:\n  - id: one\n    run: echo\n    hosts: box\n    depends-on: [z]\n"))

	if len(got) != 2 {
		t.Fatalf("got %d warnings, wanted 2: %v", len(got), got)
	}
	// Fixing one and re-running to find the next is the behaviour of a check
	// nobody finishes using.
	if !strings.Contains(got[0], "hosts") || !strings.Contains(got[1], "depends-on") {
		t.Errorf("not both, in file order: %v", got)
	}
}

// A type error is NOT an unknown field, and saying so twice is worse than once.
//
// `concurrency: not-a-number` already fails Parse with a message about the
// value. A warning beside it would be a second line about the same thing,
// phrased as though something had been ignored -- and nothing was.
func TestATypeErrorIsNotAnUnknownField(t *testing.T) {
	y := []byte("name: a\ntype: dag\nconcurrency: not-a-number\nsteps:\n  - id: one\n    run: echo\n")

	if _, err := Parse("a.yaml", y); err == nil {
		t.Fatal("the premise is gone: Parse now accepts a non-numeric concurrency")
	}
	if got := UnknownFields("a.yaml", y); len(got) != 0 {
		t.Errorf("a type error was reported as an ignored field: %v", got)
	}
}

// THE LIMIT, pinned rather than written down.
//
// `depends_on` entries decode through Dependency's own UnmarshalYAML, which
// accepts a bare string as well as a mapping -- and a custom unmarshaler is
// where KnownFields stops looking. A stray key inside one is still silently
// dropped, and this test is what will say so when that changes.
func TestACustomUnmarshalerStillHidesItsFields(t *testing.T) {
	got := UnknownFields("a.yaml", []byte(
		"name: a\ntype: dag\nsteps:\n  - id: z\n    run: echo\n  - id: one\n    run: echo\n    depends_on: [{step: z, rotulo: x}]\n"))

	if len(got) != 0 {
		t.Fatalf("KnownFields now reaches inside Dependency -- good, and this "+
			"test and its comment are the thing to update: %v", got)
	}
}

// THE ONE THAT DECIDES WHETHER THE FEATURE IS RIGHT.
//
// If the repository's own workflows warn, the warning is wrong rather than the
// files. Every directory the CI validates is here.
func TestTheRepositorysWorkflowsHaveNoUnknownFields(t *testing.T) {
	roots := []string{
		"../../../examples",
		"../../../examples/quickstart/workflows",
		"../../../examples/full-pipeline/workflows",
		"../../../examples/14-polars",
		"../../../examples/cluster/apps/workflows",
	}
	checked := 0
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml") {
				continue
			}
			path := filepath.Join(root, e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(e.Name(), raw); err != nil {
				continue // not a workflow, or already failing for its own reason
			}
			checked++
			for _, w := range UnknownFields(e.Name(), raw) {
				t.Errorf("%s", w)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d workflows were checked; the roots have moved", checked)
	}
}
