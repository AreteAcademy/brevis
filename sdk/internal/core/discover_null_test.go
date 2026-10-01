package core

import "testing"

// A nil does not DECIDE a column's type: the record that HAS a value does.
//
// The landing path never gets here with a nil -- landingColumns drops it
// before the schema or the row is built. This path is the RAW pipeline, which
// has no landing transformer, and it is the one the reporter's data would
// still have broken: `vehicle_fuel` null in record 0 and an array in record 5
// declared STRING, and the load job refused the array. [#42]
func TestANilDoesNotDecideTheType(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []any
		want    ColumnType
	}{
		{"the value comes after the nil", []any{
			map[string]any{"fuel": nil},
			map[string]any{"fuel": []any{"gasolina"}},
		}, TypeJSON},
		{"the value comes before the nil", []any{
			map[string]any{"fuel": []any{"gasolina"}},
			map[string]any{"fuel": nil},
		}, TypeJSON},
		{"the nils surround it", []any{
			map[string]any{"fuel": nil},
			map[string]any{"fuel": nil},
			map[string]any{"fuel": map[string]any{"kind": "flex"}},
			map[string]any{"fuel": nil},
		}, TypeJSON},
		{"a scalar after a nil is still a scalar", []any{
			map[string]any{"fuel": nil},
			map[string]any{"fuel": "gasolina"},
		}, TypeString},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var batch []Envelope
			for _, r := range tc.records {
				batch = append(batch, Envelope{Payload: r})
			}
			got, err := Discovered([]string{"id"}, batch)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Name != "fuel" {
				t.Fatalf("got %v, want one column `fuel`", got)
			}
			if got[0].Type != tc.want {
				t.Errorf("fuel is %s, want %s: the record that HAS a value is "+
					"the one with a shape", got[0].Type, tc.want)
			}
		})
	}
}

// The first record with a VALUE decides, and the rule after that is unchanged:
// two records disagreeing about the shape is drift, and the first one still
// wins. Only the nils are skipped.
func TestTheFirstValueStillDecidesAmongValues(t *testing.T) {
	got, err := Discovered([]string{"id"}, []Envelope{
		{Payload: map[string]any{"a": nil}},
		{Payload: map[string]any{"a": "text"}},
		{Payload: map[string]any{"a": []any{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Type != TypeString {
		t.Errorf("a is %s, want string: skipping nils must not turn into "+
			"last-write-wins among the records that do carry a value", got[0].Type)
	}
}

// A field no record gave a value declares nothing at all.
//
// The rows keep their key -- that is the row's business, and a DECLARED column
// that arrived null has to stay in it or CheckRow refuses the load. What is
// settled here is the SCHEMA: no record carried a shape, so there is no type
// to give, and guessing one is what made a column STRING that then refused
// every array. [#42]
//
// The destination resolves the mismatch: RowFields leaves the field out when
// the table has no column for it, so nothing is written and nothing refuses
// the batch.
func TestAFieldNilThroughoutDeclaresNothing(t *testing.T) {
	got, err := Discovered([]string{"id"}, []Envelope{
		{Payload: map[string]any{"id": "A", "fuel": nil}},
		{Payload: map[string]any{"id": "B", "fuel": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("declared %v from nothing but nulls: the type would be a "+
			"guess, and the first real value after it could not change it",
			got.Names())
	}
}
