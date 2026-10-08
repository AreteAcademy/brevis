package dialect

import "testing"

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
