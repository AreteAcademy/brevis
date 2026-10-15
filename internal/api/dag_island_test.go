package api_test

import (
	"regexp"
	"testing"
)

// EVERY REF THE GRAPH ISLAND WRITES IS ONE IT DECLARED.
//
// `onInit: function (instance) { rf.current = instance; }` shipped with no
// `rf` anywhere in the file: a leftover from trying `instance.fitView()`
// after the fetch, which the comment beside it explains is a no-op in React
// Flow v12 (the fix was remounting on the shape's key). Nothing read the
// instance, and every workflow and run page threw "rf is not defined" in
// React Flow's init, on master as well.
//
// A ReferenceError in a callback is invisible to every Go test -- the page
// renders, the graph draws -- so the shape is refused here instead: a
// `name.current` whose name the file never declares.
func TestTheGraphIslandWritesOnlyRefsItDeclared(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/dag.js")
	js := strip(raw)

	for _, m := range regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.current\b`).FindAllStringSubmatch(js, -1) {
		name := m[1]
		declared := regexp.MustCompile(`\b(?:var|let|const)\s+` + regexp.QuoteMeta(name) + `\b|function\s*\([^)]*\b` + regexp.QuoteMeta(name) + `\b`)
		if !declared.MatchString(js) {
			t.Errorf("dag.js uses %s.current, and %s is declared nowhere in it", name, name)
		}
	}
}
