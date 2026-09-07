package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	rt "github.com/AreteAcademy/brevis/internal/domain/runtimes"
)

// Every workflow in the repository, detected, against a written-down answer.
//
// The case table in internal/domain/runtimes tests the rules one at a time.
// This tests them against the files somebody actually wrote, and it is what
// catches a rule that becomes over-eager -- because the corpus contains steps
// that MUST stay blank. `/opt/brevis/bin/fetch-weather` is a bare binary, and
// the engine has never seen the file it points at.
//
// Adding a workflow to the repository without adding its line here fails, which
// is the point: a new file is a new case.
func TestTheRepositorysWorkflowsDetectAsExpected(t *testing.T) {
	want := map[string]string{
		// file :: step  ->  runtime [tools]
		// `action: kubernetes.run` with an image the engine cannot read.
		"analytics-dag.yaml::ingest_users": "",
		// `brevis run --select ...` -- the engine's own CLI is not in the
		// vocabulary, and claiming Go for it would be the same over-reach the
		// action rule was corrected for.
		"analytics-dag.yaml::transform_silver": "",
		"analytics-dag.yaml::gold_metrics":     "",
		"analytics-dag.yaml::gold_users":       "",
		"analytics-dag.yaml::publish":          "shell",

		"daily-report.yaml::fetch_data": "python",
		// `action: docker.run`. The image lives under `with:`, which the graph
		// does not pass in.
		"daily-report.yaml::build_report": "",
		"daily-report.yaml::notify":       "shell",

		"hello.yaml::prepare":  "shell",
		"hello.yaml::extract":  "shell",
		"hello.yaml::validate": "shell",
		"hello.yaml::publish":  "shell",

		// A bare binary path says nothing on its own -- and these two DECLARE
		// `runtime: go`, which is what the field exists for. The blank rows
		// above are the ones that keep the detector honest.
		"weather.yaml::fetch_weather": "go",
		"weather.yaml::daily_summary": "go",
	}

	roots := []string{"../../../examples", "../../../examples/quickstart/workflows"}
	seen := map[string]bool{}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			w, err := Parse(e.Name(), raw)
			if err != nil {
				t.Fatalf("%s no longer parses: %v", e.Name(), err)
			}

			for _, n := range w.Nodes {
				key := e.Name() + "::" + n.ID
				seen[key] = true

				d := rt.Resolve(n.Runtime, n.Tools, rt.Detect(n.Run, w.ImageFor(n), n.Action))
				got := d.Runtime
				if len(d.Tools) > 0 {
					got += " [" + strings.Join(d.Tools, " ") + "]"
				}

				expected, listed := want[key]
				if !listed {
					t.Errorf("%s is not in the table: a new workflow is a new case, "+
						"and it detected as %q", key, got)
					continue
				}
				if got != expected {
					t.Errorf("%s = %q, want %q", key, got, expected)
				}
			}
		}
	}

	for key := range want {
		if !seen[key] {
			t.Errorf("%s is in the table and not in the repository", key)
		}
	}
}
