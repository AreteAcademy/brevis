package pages

import "testing"

// A NUMBER IS WHAT A CELL WILL SAY, NOT WHAT GO HOLDS.
//
// Measured against both warehouses before this rule was written. BigQuery's
// REST API returns every scalar as a JSON string -- `SELECT 1, 1.5, true,
// NULL, "txt"` comes back `["1", "1.5", "true", null, "txt"]` -- and
// Postgres hands over typed values that cross the wire as JSON numbers. A
// rule reading the Go type would line up one warehouse and not the other,
// for the same query against the same data.
func TestWhatLinesUpAsANumber(t *testing.T) {
	for _, c := range []struct {
		in   any
		want bool
	}{
		{"1234", true}, // BigQuery's integer
		{"1.5", true},  // and its float
		{"-3", true},
		{".5", true},
		{"1e9", true},
		{float64(42), true}, // Postgres, through the wire
		{"hello", false},
		{"", false},
		{nil, false},
		{"true", false},
		{"NaN", false}, // a word in a text column far more often
		{"Inf", false},
		{"2026-10-10", false}, // a date is not a quantity
		{"12 apples", false},
	} {
		if got := Numeric(c.in); got != c.want {
			t.Errorf("Numeric(%#v) = %v, wanted %v", c.in, got, c.want)
		}
	}
}

// AND WHAT THE PAGER MAY CLAIM. A limit makes the rows in hand a floor, so
// the count becomes a floor too.
func TestTheTotalSaysWhenItIsAFloor(t *testing.T) {
	if got := Total(50, false); got != "50" {
		t.Errorf("a whole result says %q", got)
	}
	if got := Total(500, true); got != "500+" {
		t.Errorf("a cut result says %q, which somebody will read as a maximum", got)
	}
	if got := Total(0, false); got != "0" {
		t.Errorf("an empty result says %q", got)
	}
}
