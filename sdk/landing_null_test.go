package sdk

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// A `null` does not decide a column's type, because a null has no shape.
//
// Issue #42, finding 2. `asset_asset` was created from a first record with
// `"vehicle_fuel": null`. The rule maps "scalar, and null → TypeString", so
// the column became STRING — and the next batch carried an array:
//
//	load job failed: JSON parsing error in row starting at position 1275:
//	  Array specified for non-repeated field: vehicle_fuel.
//
// The table has 0 rows. Every batch holding one such record goes to the dead
// letter and takes its neighbours with it. Over 90 days `vehicle_fuel` was
// null 786 times and an array 62 times, so it would keep happening — and
// there is no STRING → JSON migration to undo it with.
//
// A field that is null contributes NO COLUMN and NO VALUE. The column is
// created later, by EvolveAdditiveFromPayload, from the first record where
// the field has a value and therefore a shape.
func TestANullContributesNoColumn(t *testing.T) {
	record := map[string]any{"id": "A-1", "vehicle_fuel": nil, "price": "10"}

	row, err := LandingSpread(record)
	if err != nil {
		t.Fatal(err)
	}
	// THE ROW CARRIES IT. v0.77.0 dropped it here too, and that was the
	// regression: a DECLARED column absent from the row is refused by
	// CheckRow in five drivers, which cost a consumer 5 of 23 fetchers.
	if v, present := row["vehicle_fuel"]; !present || v != nil {
		t.Errorf("the row has vehicle_fuel = %v (present=%v), want a present "+
			"nil: the row carries what the producer sent, and a declared "+
			"column that arrived null has to arrive here too", v, present)
	}
	if row["id"] != "A-1" || row["price"] != "10" {
		t.Errorf("the other fields did not survive: %v", row)
	}

	schema, err := LandingSchemaOf(record)
	if err != nil {
		t.Fatal(err)
	}
	// AND THE SCHEMA DOES NOT. A null has no shape, so the type would be a
	// guess -- and the guess was STRING, which refused every array that came
	// after it with no migration back.
	if schema.Has("vehicle_fuel") {
		t.Errorf("the schema declares vehicle_fuel from a null: %v", schema.Names())
	}

	// THE TWO DELIBERATELY DIFFER HERE, and that is the design. An earlier
	// version of this test asserted they agree, with the reasoning that the
	// agreement is what let every check downstream stay untouched. It did let
	// Reconcile and CheckRow's undeclared half stay untouched. It also made
	// the row's SHAPE depend on its VALUES, which is the premise those checks
	// are built on -- and the one declared column that arrived null then
	// looked like a chain that had stopped producing it.
	if len(row) != len(schema)+1 {
		t.Errorf("the row carries %d and the schema declares %d: the row has "+
			"exactly one more, the null. %v vs %v",
			len(row), len(schema), row, schema.Names())
	}
	for _, c := range schema {
		if _, present := row[c.Name]; !present {
			t.Errorf("the schema declares %q and the row does not carry it", c.Name)
		}
	}
}

// The reporter's sequence: null first, array later, and the array lands.
func TestTheColumnTakesItsTypeFromTheFirstRecordThatHasAValue(t *testing.T) {
	first, err := LandingSchemaOf(map[string]any{"id": "A-1", "vehicle_fuel": nil})
	if err != nil {
		t.Fatal(err)
	}
	if first.Has("vehicle_fuel") {
		t.Fatal("the creating record's null made a column")
	}

	later, err := LandingSchemaOf(map[string]any{
		"id": "A-2", "vehicle_fuel": []any{"gasolina", "etanol"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range later {
		if c.Name == "vehicle_fuel" {
			found = true
			if c.Type != TypeJSON {
				t.Errorf("vehicle_fuel is %s, want json — an array has a shape "+
					"and this is the record that has it", c.Type)
			}
		}
	}
	if !found {
		t.Error("the record with a value declared no column")
	}
}

// A field that is null in ONE record and a value in another: the batch
// declares it from the record that has it, and the null record simply does
// not carry it.
func TestANullInOneRecordDoesNotDecideForTheBatch(t *testing.T) {
	withNull, err := LandingSpread(map[string]any{"id": "A", "fuel": nil})
	if err != nil {
		t.Fatal(err)
	}
	withValue, err := LandingSpread(map[string]any{"id": "B", "fuel": []any{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	// Both rows carry the key; only the one with a VALUE declares a column.
	if v, present := withNull["fuel"]; !present || v != nil {
		t.Errorf("the null record has fuel = %v (present=%v), want a present nil",
			v, present)
	}
	if _, present := withValue["fuel"]; !present {
		t.Error("the record with a value does not carry fuel")
	}

	nullSchema, err := LandingSchemaOf(map[string]any{"id": "A", "fuel": nil})
	if err != nil {
		t.Fatal(err)
	}
	if nullSchema.Has("fuel") {
		t.Error("the null record declared a column for fuel")
	}
	valueSchema, err := LandingSchemaOf(map[string]any{"id": "B", "fuel": []any{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !valueSchema.Has("fuel") {
		t.Error("the record with a value declared no column for fuel")
	}
	// The null row then writes NULL in that column, which is the same thing
	// the null said.
}

// A null nested inside an object is the same rule, one level down.
func TestANullInsideANestedObjectContributesNoColumn(t *testing.T) {
	const marker = "BREVIS_TEST_NULLFLAT_CHILD"
	if envOn(marker) {
		row, err := LandingSpread(map[string]any{
			"name": map[string]any{"first": "Coleen", "last": nil},
		})
		if err != nil {
			t.Fatal(err)
		}
		if v, present := row["name_last"]; !present || v != nil {
			t.Errorf("name_last = %v (present=%v), want a present nil", v, present)
		}
		if row["name_first"] != "Coleen" {
			t.Errorf("name_first did not survive: %v", row)
		}
		schema, err := LandingSchemaOf(map[string]any{
			"name": map[string]any{"first": "Coleen", "last": nil},
		})
		if err != nil {
			t.Fatal(err)
		}
		if schema.Has("name_last") {
			t.Errorf("name_last was declared from a null: %v", schema.Names())
		}
		if !schema.Has("name_first") {
			t.Errorf("name_first was not declared: %v", schema.Names())
		}
		return
	}
	runChild(t, "TestANullInsideANestedObjectContributesNoColumn", marker,
		"BREVIS_NORMALIZE_DATA=true")
}

// A record that is nothing but nulls contributes nothing, and that is not an
// error: the layout's own control columns are still there, and the producer's
// fields appear the day they carry a value.
func TestARecordOfOnlyNullsDeclaresNothing(t *testing.T) {
	record := map[string]any{"a": nil, "b": nil}

	row, err := LandingSpread(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(row) != 2 {
		t.Errorf("row = %v, want both keys with nil values", row)
	}

	schema, err := LandingSchemaOf(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema) != 0 {
		t.Errorf("schema = %v, want nothing: no field has a shape", schema.Names())
	}
	if err := LandingFieldNames(map[string]any{"a": nil}); err != nil {
		t.Errorf("a record of nulls was refused: %v", err)
	}
	_ = strings.TrimSpace("")
}

// One batch through BOTH paths, which is where a null fix can go half done.
//
// The same landing batch is declared twice, by two pieces of code that never
// meet:
//
//   - the gateway DECLARES, per record, with LandingSchemaOf, and unions them
//     by name (its `merge`, first declaration wins);
//   - the driver DISCOVERS, over the ROWS the transformer produced, with
//     core.Discovered, and completes the declaration with what it finds.
//
// They have to produce the same columns with the same types, because on a
// landing table both of them run: the gateway creates it and a pipeline writing
// the same stream extends it. Where they disagree the second writer is refused
// outright -- `vehicle_fuel is json in the table and string in the declaration`.
//
// A null is exactly where they could drift: the declaration drops it in
// landingColumns, and discovery used to let it decide. Fixing one and not the
// other turns a bad column into a refused batch.
func TestTheDeclarationAndTheDiscoveryAgreeAboutNulls(t *testing.T) {
	batch := []map[string]any{
		{"id": "A-1", "vehicle_fuel": nil, "note": nil, "meta": map[string]any{"uf": "SP"}},
		{"id": "A-2", "vehicle_fuel": nil, "note": nil},
		{"id": "A-3", "vehicle_fuel": []any{"gasolina", "etanol"}, "note": nil, "price": "11"},
		{"id": "A-4", "vehicle_fuel": nil, "note": nil},
	}

	// The gateway's side: per record, unioned by name, first declaration wins.
	declared := map[string]ColumnType{}
	for i, record := range batch {
		schema, err := LandingSchemaOf(record)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		for _, c := range schema {
			if _, seen := declared[c.Name]; !seen {
				declared[c.Name] = c.Type
			}
		}
	}

	// The driver's side: over the rows, which is all it ever sees.
	control := LandingControlColumns(LandingOptions{}).Names()
	var rows []core.Envelope
	for i, record := range batch {
		row, err := LandingSpread(record)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		rows = append(rows, core.Envelope{Payload: row})
	}
	found, err := core.Discovered(control, rows)
	if err != nil {
		t.Fatal(err)
	}
	discovered := map[string]ColumnType{}
	for _, c := range found {
		discovered[c.Name] = c.Type
	}

	for name, want := range declared {
		got, present := discovered[name]
		if !present {
			t.Errorf("the declaration has %q and the discovery does not: the "+
				"gateway creates that column and a pipeline writing the same "+
				"stream would not know to", name)
			continue
		}
		if got != want {
			t.Errorf("%s: declared %s, discovered %s. On a shared table the "+
				"second writer is refused by name", name, want, got)
		}
	}
	for name := range discovered {
		if _, present := declared[name]; !present {
			t.Errorf("the discovery has %q and the declaration does not", name)
		}
	}

	// And the answer both of them give is the one the shape supports.
	if want := map[string]ColumnType{
		"id": TypeString, "vehicle_fuel": TypeJSON,
		"meta": TypeJSON, "price": TypeString,
	}; !reflect.DeepEqual(declared, want) {
		t.Errorf("the batch declares %v, want %v.\n\n`note` is null in every "+
			"record and must not be there at all; `vehicle_fuel` is null in "+
			"three of four and takes the shape of the one that is not",
			declared, want)
	}
}
