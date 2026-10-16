package pages

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A STAGE SHOWS THE NUMBERS ITS GRAPH NODE SHOWS (screen-run-v2 RV3): the
// same two, in the same order, formatted the same way -- the one-step row
// and a graph's stage pill are two drawings of one thing.
func TestStageNumbersAreTheGraphsTwo(t *testing.T) {
	got := StageNumbers(map[string]any{"objects": 1.0, "rows": 12345.0, "pages": 1.0, "detail": "/data/x.csv", "bytes": 61.0})
	if len(got) != 2 || got[0] != (StageNumber{"rows", "12,345"}) || got[1] != (StageNumber{"pages", "1"}) {
		t.Errorf("numbers = %v", got)
	}
	if got := StageNumbers(map[string]any{"in": 370.0, "out": "370"}); len(got) != 2 || got[1].Value != "370" {
		t.Errorf("a string number = %v", got)
	}
	if StageNumbers(nil) != nil {
		t.Error("no numbers is not nothing")
	}
}

// THE GO SIDE AND dag.js CANNOT DRIFT: the highlight order and the stage
// states' colours and the runtimes' labels are read out of the island and compared.
func TestTheStageRulesMatchDagJs(t *testing.T) {
	raw, err := os.ReadFile("../assets/dag.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	m := regexp.MustCompile(`var HIGHLIGHT = \[([^\]]*)\]`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("dag.js has no HIGHLIGHT")
	}
	var order []string
	for _, k := range strings.Split(m[1], ",") {
		order = append(order, strings.Trim(strings.TrimSpace(k), `"`))
	}
	if !slices.Equal(order, StageHighlight) {
		t.Errorf("dag.js highlights %v, Go %v", order, StageHighlight)
	}
	c := regexp.MustCompile(`var STAGE_STATE_COLOURS = \{([^}]*)\}`).FindStringSubmatch(js)
	if c == nil {
		t.Fatal("dag.js has no STAGE_STATE_COLOURS")
	}
	pairs := regexp.MustCompile(`(\w+): "(\w+)"`).FindAllStringSubmatch(c[1], -1)
	if len(pairs) != len(StageStates) {
		t.Errorf("dag.js maps %d stage states, Go %d", len(pairs), len(StageStates))
	}
	for _, p := range pairs {
		if StageStates[p[1]] != p[2] {
			t.Errorf("stage state %s: dag.js %s, Go %s", p[1], p[2], StageStates[p[1]])
		}
	}
	l := regexp.MustCompile(`(?s)var LANG_LABEL = \{(.*?)\};`).FindStringSubmatch(js)
	if l == nil {
		t.Fatal("dag.js has no LANG_LABEL")
	}
	labels := regexp.MustCompile(`(\w+): "([^"]+)"`).FindAllStringSubmatch(l[1], -1)
	if len(labels) != len(RuntimeLabels) {
		t.Errorf("dag.js labels %d runtimes, Go %d", len(labels), len(RuntimeLabels))
	}
	for _, p := range labels {
		if RuntimeLabels[p[1]] != p[2] {
			t.Errorf("runtime %s: dag.js %q, Go %q", p[1], p[2], RuntimeLabels[p[1]])
		}
	}
}
