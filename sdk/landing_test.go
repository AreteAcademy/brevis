package sdk

import "testing"

// The landing id is FROZEN, and this is the only thing that says so.
//
// It is a characterisation test on purpose: the values below were read from
// the implementation, and that is the point. Every part of the formula --
// the "auto_table" namespace, the field order, the "|" separator, the
// canonical fingerprint -- is already written into rows that exist. The test
// does not argue that these are the right ids; it argues that they are the
// ids, and that changing any of them is a decision somebody makes on purpose
// rather than a tidy-up nobody notices.
//
// It was written because a mutation survived without it. Renaming
// LandingProvider from "auto_table" to "landing" -- which is exactly the
// "correction" the constant's own comment warns about -- passed the whole
// gateway suite, because the tests there compare the constant against
// itself. The day somebody makes that rename, every id already written
// changes and the next merge duplicates the table.
func TestTheLandingIDIsFrozen(t *testing.T) {
	// Deliberately awkward: a float, an array whose order matters, and a
	// nested object whose keys are out of order, so the canonical form is
	// doing work.
	record := map[string]any{
		"id":     "A-1",
		"total":  15.5,
		"tags":   []any{"x", "y"},
		"nested": map[string]any{"b": 2, "a": 1},
	}

	for _, c := range []struct {
		name, key, want string
	}{
		{"keyed", "A-1", "cf239eb8-b105-5bdd-98a2-7d25cf776170"},
		// No key: the content is the key, in both slots.
		{"keyless", "", "446c3555-9e99-5781-b80c-ac8868cd54b6"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := LandingID("landing.orders", c.key, record)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("LandingID = %s, want %s\n\n"+
					"If this changed on purpose, every landing row already "+
					"written has the old id and the next merge will duplicate "+
					"the table. If it changed by accident -- a renamed "+
					"namespace, a reordered field, a fingerprint that stopped "+
					"being canonical -- this is the test doing its job.",
					got, c.want)
			}
		})
	}
}

// Map iteration is randomised, so a fingerprint that is not canonical gives a
// different id on every call for the same record. That is the one thing it
// exists to prevent.
func TestTheLandingIDDoesNotMoveBetweenCalls(t *testing.T) {
	record := map[string]any{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6}
	first, err := LandingID("t", "k", record)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		got, err := LandingID("t", "k", record)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("two calls, two ids: %s then %s", first, got)
		}
	}
}

// A record that differs anywhere gets a different id, because the last slot
// is the content.
func TestTheLandingIDFollowsTheRecord(t *testing.T) {
	base := map[string]any{"id": "A", "v": 1}
	a, _ := LandingID("t", "A", base)
	b, _ := LandingID("t", "A", map[string]any{"id": "A", "v": 2})
	if a == b {
		t.Error("two different records share an id: the fingerprint is not " +
			"reaching the formula, and a changed record would be absorbed as " +
			"a repeat of the old one")
	}
	// And the array's order is significant: [1,2] is not [2,1].
	x, _ := LandingID("t", "A", map[string]any{"xs": []any{1, 2}})
	y, _ := LandingID("t", "A", map[string]any{"xs": []any{2, 1}})
	if x == y {
		t.Error("[1,2] and [2,1] share an id: the canonical form is sorting " +
			"arrays, and they are different documents")
	}
}
