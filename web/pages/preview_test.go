package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// NO SERVICE, NO TAB. A console that was never told where `serve` lives shows
// the destination exactly as it did before this existed -- a tab that always
// says "not configured" is a tab nobody wants and a question nobody can act
// on.
func TestWithNoServiceThereIsNoTab(t *testing.T) {
	v := TargetView{}
	if v.HasPreview() {
		t.Error("a view with no preview offered one")
	}
}

// A GRID DRAWS WHAT ARRIVED, and NULL is not the empty string. One is
// "nothing was recorded" and the other is a value somebody wrote; a grid that
// prints both as blank is a grid that loses the difference.
func TestACellSaysWhichKindOfNothingItIs(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		// THE WORD AND NOT A DASH, changed with the result panel: both
		// distinguish a NULL from an empty string, and only one of them
		// tells the reader WHICH. In a text column a dash can be a value
		// somebody wrote.
		{nil, "null"},
		{"", ""},
		{"a", "a"},
		{"1767484800.0", "1767484800.0"},
		{int64(3), "3"},
		{true, "true"},
	} {
		if got := Cell(c.in); got != c.want {
			t.Errorf("Cell(%#v) = %q, wanted %q", c.in, got, c.want)
		}
	}
}

// TRUNCATION IS SAID ON THE SCREEN, not only on the wire. The service goes to
// the trouble of knowing it cut; a page that dropped the fact would make that
// work pointless -- somebody reads a MAX off the grid and is wrong.
func TestTheViewSaysItWasCut(t *testing.T) {
	v := TargetView{Preview: &sqlserve.Result{
		Columns: []string{"k"}, Rows: [][]any{{1}}, Truncated: true, Limit: 1,
	}}
	if !v.HasPreview() {
		t.Fatal("a view with a result does not offer the tab")
	}
	note := v.PreviewNote()
	if !strings.Contains(note, "1") {
		t.Errorf("the note does not say how many were shown: %q", note)
	}
	if !strings.Contains(strings.ToLower(note), "more") {
		t.Errorf("the note does not say there is more: %q", note)
	}

	v.Preview.Truncated = false
	if n := v.PreviewNote(); strings.Contains(strings.ToLower(n), "more") {
		t.Errorf("an untruncated preview claimed there was more: %q", n)
	}
}

// A REASON IS A TAB TOO. A destination `serve` refuses -- not a table, no
// connection -- still gets the tab, with the sentence where the grid would
// be. The alternative is a tab that opens onto nothing.
func TestAReasonCountsAsSomethingToShow(t *testing.T) {
	v := TargetView{PreviewErr: "this target cannot be previewed: only BigQuery destinations can be read today"}
	if !v.HasPreview() {
		t.Error("a view holding a reason offered no tab")
	}
}
