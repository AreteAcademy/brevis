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

// The layout a pipeline composes into Target.Schema.
//
// Nine columns: the eight the layout owns and the one the producer does. A
// caller who wants their own columns instead of the JSON one takes
// LandingControlColumns and appends theirs.
func TestLandingSchemaIsTheWholeTable(t *testing.T) {
	got := LandingSchema(LandingOptions{})

	want := []string{
		LandingColumnID, LandingColumnRecordKey, LandingColumnOperation,
		LandingColumnReceivedAt, LandingColumnLoadedAt, LandingColumnStream,
		LandingColumnGateway, LandingColumnReceivedBytes, LandingColumnData,
	}
	if len(got) != len(want) {
		t.Fatalf("%d columns, want %d: %v", len(got), len(want), got.Names())
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("column %d is %q, want %q -- the ORDER is the DDL's, and "+
				"a table created here has to match one the gateway created",
				i, got[i].Name, name)
		}
	}

	// The two the gateway fills and a pipeline cannot must be nullable, or a
	// pipeline could not write the row at all.
	for _, name := range []string{LandingColumnStream, LandingColumnGateway} {
		for _, c := range got {
			if c.Name == name && c.Required {
				t.Errorf("%s is NOT NULL: a pipeline has no gateway and no "+
					"stream, and a required column it cannot fill is a table "+
					"it cannot write to", name)
			}
		}
	}

	// The control columns are the same eight, without the producer's one.
	control := LandingControlColumns(LandingOptions{})
	if len(control) != len(got)-1 {
		t.Fatalf("%d control columns against %d in the table", len(control), len(got))
	}
	for i := range control {
		if control[i].Name != got[i].Name {
			t.Errorf("control column %d is %q, the table's is %q",
				i, control[i].Name, got[i].Name)
		}
	}
	if control.Has(LandingColumnData) {
		t.Error("the control columns include the producer's column")
	}
}

// The options are what the write mode makes vary, and both are the same
// decision seen twice.
func TestLandingOptionsReachTheColumns(t *testing.T) {
	plain := LandingSchema(LandingOptions{})
	for _, c := range plain {
		if c.Name == LandingColumnID && c.Unique {
			t.Error("the id is UNIQUE by default: BigQuery refuses the " +
				"constraint and `append` does not want it")
		}
		if c.Name == LandingColumnRecordKey && c.Required {
			t.Error("the record key is NOT NULL by default: under `append` a " +
				"record may name none, and the database would refuse the row")
		}
	}

	both := LandingSchema(LandingOptions{UniqueID: true, Keyed: true})
	var sawUnique, sawKeyed bool
	for _, c := range both {
		if c.Name == LandingColumnID && c.Unique {
			sawUnique = true
		}
		if c.Name == LandingColumnRecordKey && c.Required {
			sawKeyed = true
		}
	}
	if !sawUnique {
		t.Error("UniqueID did not reach brevis_ingestion_id")
	}
	if !sawKeyed {
		t.Error("Keyed did not reach brevis_record_key")
	}
}

// Composing is the point: the default is the caller declaring the table, and
// this only saves them typing the part that already exists.
func TestALandingSchemaComposes(t *testing.T) {
	mine := append(LandingSchema(LandingOptions{}),
		Column{Name: "tenant", Type: TypeString, Required: true})

	if !mine.Has("tenant") {
		t.Fatal("an appended column did not survive")
	}
	if !mine.Has(LandingColumnID) || !mine.Has(LandingColumnData) {
		t.Fatal("appending lost the layout")
	}
	// Two callers appending must not write into one array.
	//
	// The first version of this check asked whether a later call could SEE
	// an earlier caller's column, and a shared backing slice passed it: the
	// appended column sits past the returned length, so nobody sees it until
	// somebody else appends over it. The failure is a clobber, not a
	// sighting, and it takes two writers to show.
	a := append(LandingSchema(LandingOptions{}), Column{Name: "tenant_a", Type: TypeString})
	b := append(LandingSchema(LandingOptions{}), Column{Name: "tenant_b", Type: TypeString})
	if got := a[len(a)-1].Name; got != "tenant_a" {
		t.Errorf("after a second caller appended, the first caller's last "+
			"column is %q: the two schemas share a backing array, and one "+
			"pipeline's column lands in another's table", got)
	}
	if got := b[len(b)-1].Name; got != "tenant_b" {
		t.Errorf("the second caller's last column is %q", got)
	}
}
