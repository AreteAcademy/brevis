package dialect

import (
	"fmt"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/model"
)

// Rebuild is DERIVED and never stored, which is the whole reason it is a
// method. Three inputs decide it and a fourth field holding the answer would
// be a second place for it to be wrong.
func TestWhatCountsAsStartingOver(t *testing.T) {
	for _, c := range []struct {
		name string
		st   State
		want bool
	}{
		{"nothing is there", State{Current: Absent}, true},
		{"a table is there", State{Current: Table}, false},
		// A VIEW CANNOT BE MERGED INTO. A model that was a view yesterday and
		// is incremental today has no rows to add to -- it has a relation of
		// the wrong kind, and the only way forward is to create the table.
		{"a view is there", State{Current: View}, true},
		{"full refresh over a table", State{Current: Table, FullRefresh: true}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.st.Rebuild(); got != c.want {
				t.Errorf("Rebuild() = %v, wanted %v", got, c.want)
			}
		})
	}
}

// Without a lookback the filter is today's exact SQL; with one the bound moves
// back and the comparison is inclusive.
func TestNewRowsLookback(t *testing.T) {
	m := model.Model{Schema: "s", Name: "t", Watermark: "w"}
	minus := func(e string, secs int64) string { return fmt.Sprintf("MINUS(%s,%d)", e, secs) }

	const max = "(SELECT MAX(w) FROM s.t)"
	want := "SELECT * FROM (\nq\n) AS brevis_new\n WHERE " + max + " IS NULL\n    OR w > " + max
	if got := NewRows(m, "q", minus); got != want {
		t.Errorf("no lookback:\n%s\nwanted:\n%s", got, want)
	}
	m.Lookback = 2 * time.Hour
	want = "SELECT * FROM (\nq\n) AS brevis_new\n WHERE " + max + " IS NULL\n    OR w >= MINUS(" + max + ",7200)"
	if got := NewRows(m, "q", minus); got != want {
		t.Errorf("lookback:\n%s\nwanted:\n%s", got, want)
	}
}
