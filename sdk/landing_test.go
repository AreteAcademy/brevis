package sdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
	for _, name := range []string{
		LandingColumnStream, LandingColumnGateway,
		// The gateway's number counts the WIRE, envelope included. The
		// record's own JSON is about 30% smaller, so filling this would give
		// one column two meanings and make a sum across both paths wrong by
		// whatever share came from which.
		LandingColumnReceivedBytes,
	} {
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

func landingRow(t *testing.T, in map[string]any, table string, opts ...LandingArg) map[string]any {
	t.Helper()
	out, err := Landing(table, opts...)(in)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Landing returned %T", out)
	}
	return row
}

// The transformer composes exactly the columns LandingSchema declares.
//
// A declared column the chain does not deliver is an error at load naming the
// column; a field the row carries that nothing declared is an error naming
// the field. The two have to agree or neither is worth having.
func TestLandingFillsWhatLandingSchemaDeclares(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A-1", "total": 10},
		"landing.orders", LandingKey("id"))

	// Every declared column, EXCEPT the one the destination fills.
	//
	// brevis_loaded_at is declared so the table gets created with it and a
	// DEFAULT; sending it -- even as NULL -- overrides that default and
	// costs the one measurement the pair exists for. It is the only
	// exception, and naming it here is what keeps it from growing.
	const destinationFills = LandingColumnLoadedAt

	declared := LandingSchema(LandingOptions{})
	if len(row) != len(declared)-1 {
		t.Fatalf("the row has %d columns and the schema declares %d, of which "+
			"exactly one is the destination's:\nrow: %v", len(row), len(declared), row)
	}
	for _, c := range declared {
		_, carried := row[c.Name]
		if c.Name == destinationFills {
			if carried {
				t.Errorf("%q is in the row: it is a database DEFAULT, and "+
					"sending anything overrides it", c.Name)
			}
			continue
		}
		if !carried {
			t.Errorf("the schema declares %q and the row does not carry it", c.Name)
		}
	}
}

// The id comes from the one function that mints it. A second implementation
// here is how two paths stop agreeing.
func TestLandingMintsTheSameIDAsTheFunction(t *testing.T) {
	record := map[string]any{"id": "A-1", "total": 10}
	// The record the fingerprint is over is the PRODUCER's, before any
	// control column is added -- the gateway hashes `data`, not the row.
	want, err := LandingID("landing.orders", "A-1", map[string]any{"id": "A-1", "total": 10})
	if err != nil {
		t.Fatal(err)
	}
	row := landingRow(t, record, "landing.orders", LandingKey("id"))
	if got := row[LandingColumnID]; got != want {
		t.Errorf("the transformer minted %v, LandingID gives %s -- they are "+
			"two implementations of one formula, which is the thing this "+
			"whole layout exists to avoid", got, want)
	}
}

// The gateway's two columns are NULL, and that is the signal.
func TestLandingLeavesTheGatewaysColumnsEmpty(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A"}, "t", LandingKey("id"))
	for _, name := range []string{LandingColumnStream, LandingColumnGateway} {
		v, present := row[name]
		if !present {
			t.Errorf("%s is missing from the row; the schema declares it and "+
				"the load would refuse the batch", name)
		}
		if v != nil {
			t.Errorf("%s is %v, want nil -- a pipeline has no gateway and no "+
				"stream, and a plausible-looking value there is a lie in a "+
				"column somebody aggregates", name, v)
		}
	}
}

// INSERT unless the record says otherwise.
func TestLandingsOperation(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A"}, "t", LandingKey("id"))
	if got := row[LandingColumnOperation]; got != LandingInsert {
		t.Errorf("operation = %v, want %s", got, LandingInsert)
	}

	cdc := landingRow(t, map[string]any{"id": "A", "op": "DELETE"}, "t",
		LandingKey("id"), LandingOperationFrom("op"))
	if got := row[LandingColumnOperation]; got != LandingInsert {
		t.Errorf("the default moved: %v", got)
	}
	if got := cdc[LandingColumnOperation]; got != LandingDelete {
		t.Errorf("operation = %v, want %s -- a source that computes deletes "+
			"has to be able to say so", got, LandingDelete)
	}

	if _, err := Landing("t", LandingOperationFrom("op"))(
		map[string]any{"id": "A", "op": "UPSERT"}); err == nil {
		t.Error("an operation outside the vocabulary was accepted: a landing " +
			"table's history is only readable if the verb set is closed")
	}
}

// With no key the content is the key, exactly as `append` does in the gateway.
func TestLandingWithoutAKey(t *testing.T) {
	record := map[string]any{"a": 1}
	want, _ := LandingID("t", "", map[string]any{"a": 1})
	row := landingRow(t, record, "t")
	if got := row[LandingColumnID]; got != want {
		t.Errorf("keyless id = %v, want %s", got, want)
	}
	if got := row[LandingColumnRecordKey]; got != nil {
		t.Errorf("record_key = %v, want nil: the record named none, and "+
			"inventing one would make it look like the producer's", got)
	}
}

// The prefix is reserved, and the SDK refuses rather than overwrites.
//
// The gateway overwrites, deliberately, because its producer is a stranger
// who must not be able to forge a control field. Here the producer is the
// pipeline's own author: silently replacing what they wrote would hide their
// mistake instead of naming it, which is what IngestionLoadedAt already does.
func TestLandingRefusesAReservedKey(t *testing.T) {
	_, err := Landing("t", LandingKey("id"))(map[string]any{
		"id": "A", LandingColumnReceivedAt: "2020-01-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("a record carrying a brevis_ field was accepted")
	}
	if !strings.Contains(err.Error(), LandingColumnReceivedAt) {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// The record lands whole, and comes back.
func TestLandingKeepsTheRecord(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A", "nested": map[string]any{"x": 1}},
		"t", LandingKey("id"))
	var back map[string]any
	if err := json.Unmarshal([]byte(row[LandingColumnData].(string)), &back); err != nil {
		t.Fatalf("the data column is not JSON: %v", err)
	}
	if back["id"] != "A" {
		t.Errorf("the record did not survive: %v", back)
	}
	if _, leaked := back[LandingColumnID]; leaked {
		t.Error("a control column leaked into the producer's data")
	}
}

// received_at is the SDK's clock, in the format the sibling column uses.
func TestLandingStampsItsOwnClock(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A"}, "t", LandingKey("id"))
	at, ok := row[LandingColumnReceivedAt].(string)
	if !ok {
		t.Fatalf("received_at is %T", row[LandingColumnReceivedAt])
	}
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("received_at is not RFC 3339: %q", at)
	}
	if time.Since(when) > time.Minute || when.After(time.Now().Add(time.Minute)) {
		t.Errorf("received_at is %s, which is not now", at)
	}
	// loaded_at must be ABSENT, not nil. An explicit NULL overrides the
	// column's DEFAULT -- the first version of this asserted "present and
	// nil", which is exactly the bug, and only the integration test caught
	// it because this one looks at the map instead of the table.
	if v, sent := row[LandingColumnLoadedAt]; sent {
		t.Errorf("loaded_at is in the row as %v: it is a database DEFAULT, "+
			"and sending anything -- NULL included -- overrides it", v)
	}
}
