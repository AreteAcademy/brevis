package pages

import "testing"

// A REORDERING IS A BLANK EDITOR, and this is the only part of that a Go test
// can hold. The browser evaluating these in order is not observable here; the
// LIST is, and a list in the wrong order is the mistake.
//
// PAIR BY PAIR AND NOT ONE SEQUENCE, so a failure names the two files that
// stopped agreeing rather than printing a slice and leaving the reader to
// work out which edge moved.
func TestTheEditorsAssetsLoadAfterWhatTheyExtend(t *testing.T) {
	at := map[string]int{}
	for i, src := range EditorAssets() {
		at[src] = i
	}

	for _, dep := range []struct{ first, then, cost string }{
		{"/assets/vendor/codemirror.js", "/assets/vendor/codemirror-sql.js",
			"the SQL mode registers itself with CodeMirror, so what is left is a plain textarea"},
		{"/assets/vendor/codemirror.js", "/assets/vendor/show-hint.js",
			"show-hint hangs showHint off CodeMirror, and there would be nothing to hang it off"},
		{"/assets/vendor/show-hint.js", "/assets/vendor/sql-hint.js",
			"sql-hint fills a list that show-hint is what opens"},
		{"/assets/vendor/codemirror-sql.js", "/assets/vendor/sql-hint.js",
			"sql-hint declares the mode as its dependency and takes its keywords from the mode's configuration"},
		{"/assets/vendor/sql-hint.js", "/assets/sql.js",
			"this screen asks for CodeMirror.hint.sql before anything has defined it"},
	} {
		i, ok := at[dep.first]
		if !ok {
			t.Errorf("the island no longer loads %s", dep.first)
			continue
		}
		j, ok := at[dep.then]
		if !ok {
			t.Errorf("the island no longer loads %s", dep.then)
			continue
		}
		if i > j {
			t.Errorf("%s loads before %s: %s", dep.then, dep.first, dep.cost)
		}
	}
}
