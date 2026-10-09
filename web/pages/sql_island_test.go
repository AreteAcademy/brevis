package pages

import "testing"

// A REORDERING IS A BLANK EDITOR, and this is the only part of that a Go test
// can hold. The browser evaluating these in order is not observable here; the
// LIST is, and a list in the wrong order is the mistake.
func TestTheEditorsCoreLoadsBeforeItsMode(t *testing.T) {
	js := EditorAssets()

	core, mode, island := -1, -1, -1
	for i, src := range js {
		switch src {
		case "/assets/vendor/codemirror.js":
			core = i
		case "/assets/vendor/codemirror-sql.js":
			mode = i
		case "/assets/sql.js":
			island = i
		}
	}
	if core < 0 || mode < 0 || island < 0 {
		t.Fatalf("the island no longer loads the three files this is about: %v", js)
	}
	if core > mode {
		t.Error("the SQL mode loads before CodeMirror defines it, which leaves a plain textarea")
	}
	if mode > island {
		t.Error("this screen's own script runs before the mode it asks for")
	}
}
