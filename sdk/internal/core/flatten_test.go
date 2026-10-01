package core

import (
	"sort"
	"strings"
	"testing"
)

func names(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The brief's own example, which settles more than it looks like it does.
//
//	data = [
//	    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
//	    {"name": {"given": "Mark", "family": "Regner"}},
//	    {"id": 2, "name": "Faye Raker"},
//	]
//	→ id name_first name_last name_given name_family name
//
// `name` appears BOTH flattened and whole, because the third record carries
// it as a scalar. A field that is an object in one record and a scalar in
// another produces both, and that is pandas' answer as well as the one the
// example asks for.
func TestFlattenOneLevelOnTheBriefsExample(t *testing.T) {
	records := []map[string]any{
		{"id": float64(1), "name": map[string]any{"first": "Coleen", "last": "Volk"}},
		{"name": map[string]any{"given": "Mark", "family": "Regner"}},
		{"id": float64(2), "name": "Faye Raker"},
	}

	union := map[string]bool{}
	for i, r := range records {
		got, err := FlattenOneLevel(r)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		for k := range got {
			union[k] = true
		}
	}

	want := []string{"id", "name", "name_family", "name_first", "name_given", "name_last"}
	var got []string
	for k := range union {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("columns:\n  got  %v\n  want %v", got, want)
	}

	// And the values went with the names.
	first, _ := FlattenOneLevel(records[0])
	if first["name_first"] != "Coleen" || first["name_last"] != "Volk" {
		t.Errorf("record 0 = %v", first)
	}
	third, _ := FlattenOneLevel(records[2])
	if third["name"] != "Faye Raker" {
		t.Errorf("record 2 kept name as %v", third["name"])
	}
}

// ONE level. A deeper object is a value, not a longer path.
func TestFlattenOneLevelStopsAtOne(t *testing.T) {
	got, err := FlattenOneLevel(map[string]any{"a": map[string]any{"b": map[string]any{"c": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if n := names(got); len(n) != 1 || n[0] != "a_b" {
		t.Fatalf("columns %v, want [a_b] -- a_b_c would be two levels", n)
	}
	inner, ok := got["a_b"].(map[string]any)
	if !ok || inner["c"] != 1 {
		t.Errorf("a_b = %#v, want the object itself: one level means the "+
			"second one stays a value, and a value that is an object becomes "+
			"a JSON column", got["a_b"])
	}
}

// An array is a value and never a path. `{"a": [1,2]}` is one column, not
// a_0 and a_1 -- a record that wants one row per element wants ArrayAt, which
// is a different operation with a different name.
func TestFlattenOneLevelLeavesArraysAlone(t *testing.T) {
	got, err := FlattenOneLevel(map[string]any{"a": []any{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if n := names(got); len(n) != 1 || n[0] != "a" {
		t.Errorf("columns %v, want [a]", n)
	}
}

// Lower-cased, and that is every field and not only the nested ones.
func TestFlattenOneLevelLowerCases(t *testing.T) {
	got, err := FlattenOneLevel(map[string]any{
		"userName": map[string]any{"firstName": "x"},
		"ID":       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "username_firstname"}
	if n := names(got); strings.Join(n, " ") != strings.Join(want, " ") {
		t.Errorf("columns %v, want %v -- joined and lower-cased, not split on "+
			"camelCase: `user_name_first_name` is the other reading and it was "+
			"decided against, because acronyms have no agreed answer", n, want)
	}
}

// A field the producer sent does not vanish.
func TestFlattenOneLevelKeepsAnEmptyObject(t *testing.T) {
	got, err := FlattenOneLevel(map[string]any{"a": map[string]any{}, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := names(got); len(n) != 2 {
		t.Fatalf("columns %v: an empty object has nothing to flatten, and "+
			"dropping it would make a field the producer SENT disappear from "+
			"the table with nothing saying so", n)
	}
	if m, ok := got["a"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("a = %#v, want the empty object itself", got["a"])
	}
}

// null inside a nested object is a column holding NULL, not a missing one.
func TestFlattenOneLevelKeepsNull(t *testing.T) {
	got, err := FlattenOneLevel(map[string]any{"a": map[string]any{"b": nil}})
	if err != nil {
		t.Fatal(err)
	}
	v, present := got["a_b"]
	if !present || v != nil {
		t.Errorf("a_b = %v (present=%v): null and absent are different facts, "+
			"and a column cannot tell them apart afterwards", v, present)
	}
}

// A collision is refused BY NAME, never resolved by whoever wins the map.
//
// Go's map iteration is randomised, so last-write-wins would give a different
// answer per run: the same batch would produce a different table on Tuesday.
func TestFlattenOneLevelRefusesACollision(t *testing.T) {
	for _, c := range []struct {
		name   string
		record map[string]any
		both   []string
	}{
		{
			"a flattened path meets a literal key",
			map[string]any{"name_first": 1, "name": map[string]any{"first": 2}},
			[]string{"name_first", "name"},
		},
		{
			"two keys that lower-case to one",
			map[string]any{"Name": 1, "name": 2},
			[]string{"Name", "name"},
		},
		{
			"two nested paths that lower-case to one",
			map[string]any{
				"user": map[string]any{"Name": 1},
				"USER": map[string]any{"name": 2},
			},
			[]string{"user", "USER"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := FlattenOneLevel(c.record)
			if err == nil {
				t.Fatal("accepted: whichever key the map yielded last would " +
					"win, and the map yields them in a different order every run")
			}
			for _, want := range c.both {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

// A flattened name still has to be a column name.
func TestFlattenOneLevelRefusesAnIllegalName(t *testing.T) {
	// Legal on its own -- `LandingFieldNames` would pass `a` -- and illegal
	// once joined. The check has to run on what the COLUMN will be called.
	_, err := FlattenOneLevel(map[string]any{"a": map[string]any{"b c": 1}})
	if err == nil {
		t.Fatal("`a_b c` was accepted as a column name")
	}
	if !strings.Contains(err.Error(), "a_b c") {
		t.Errorf("the refusal does not name the column it would have made: %v", err)
	}
}
