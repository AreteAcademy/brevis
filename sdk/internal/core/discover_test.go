package core

import (
	"strings"
	"testing"
)

func batch(records ...map[string]any) []Envelope {
	out := make([]Envelope, 0, len(records))
	for _, r := range records {
		out = append(out, Envelope{Payload: r})
	}
	return out
}

// What the batch carries and the declaration does not.
func TestDiscovered(t *testing.T) {
	declared := []string{"brevis_ingestion_id", "source_key"}
	found, err := Discovered(WriteOptions{Columns: declared}, batch(map[string]any{
		"brevis_ingestion_id": "x",
		"source_key":          "k",
		"valor":               8.89,
		"meta":                map[string]any{"uf": "SP"},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if len(found) != 2 {
		t.Fatalf("%d discovered, want 2: %v", len(found), found.Names())
	}
	// Sorted, so the same batch always declares the same DDL.
	if found[0].Name != "meta" || found[0].Type != TypeJSON {
		t.Errorf("first is %v", found[0])
	}
	if found[1].Name != "valor" || found[1].Type != TypeString {
		t.Errorf("second is %v", found[1])
	}

	// NEVER Required, and the ALTER path is not where that matters.
	//
	// AlterTable forces an added column nullable whatever the declaration
	// says, so `Required: true` here is invisible on a table that already
	// exists -- a mutation setting it passed the whole MySQL suite. The
	// CREATE path is where it bites: a table created from the extended
	// declaration would get NOT NULL on a column the NEXT batch need not
	// carry, and the batch after the one it helped would be refused by the
	// database.
	for _, c := range found {
		if c.Required {
			t.Errorf("%s is declared NOT NULL: a column the batch contributed "+
				"is one another record may legitimately lack", c.Name)
		}
	}
}

// The UNION of the batch, never the first record.
//
// CheckRow looks at records[0] and that is enough for what it does -- every
// record comes out of one Transform chain. This is not that: a batch holds N
// records for one table and they need not carry the same fields, and a field
// that appears only in the last one is still a column the table needs. Miss
// it and the row is refused at the destination, by Reconcile, naming a field
// nobody declared.
func TestDiscoveredIsTheUnionOfTheBatch(t *testing.T) {
	records := make([]map[string]any, 50)
	for i := range records {
		records[i] = map[string]any{"id": "A"}
	}
	records[49]["late"] = "arrived last"

	found, err := Discovered(WriteOptions{Columns: []string{"id"}}, batch(records...))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "late" {
		t.Fatalf("the union missed a field that only the last record carries: %v",
			found.Names())
	}
}

// A field that cannot be a column name is refused here, before any DDL.
//
// The landing transformer already refuses it, but nothing makes this mode
// landing-only: set it on a raw pipeline and `meu-campo` would reach the DDL.
// Postgres would quote it and create it, and the same fetcher would break the
// day somebody points it at BigQuery.
func TestDiscoveredRefusesAFieldThatCannotBeAColumn(t *testing.T) {
	_, err := Discovered(WriteOptions{Columns: []string{"id"}}, batch(map[string]any{"id": "A", "meu-campo": 1}))
	if err == nil {
		t.Fatal("\"meu-campo\" was accepted as a column name")
	}
	if !strings.Contains(err.Error(), "meu-campo") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
	// A field the declaration ALREADY has is not checked again: it is the
	// consumer's own name, already in their table.
	if _, err := Discovered(WriteOptions{Columns: []string{"ok-name"}}, batch(map[string]any{"ok-name": 1})); err != nil {
		t.Errorf("a declared column was re-judged by the discovery rule: %v", err)
	}
}

// The extension must not write into the caller's slices.
//
// WriteOptions travels by value, so extending `opt` is per-call -- but its
// Schema is a slice, and append can write past the length into the backing
// array somebody else is holding. That is the clobber TestALandingSchemaComposes
// exists for, one layer up.
func TestWithDiscoveredDoesNotAliasTheCaller(t *testing.T) {
	base := Schema{{Name: "a", Type: TypeString}, {Name: "b", Type: TypeString}}
	// Room to spare, which is exactly when append writes in place.
	shared := make(Schema, 2, 8)
	copy(shared, base)

	opt := WriteOptions{Columns: []string{"a", "b"}, Schema: shared}

	first := WithDiscovered(opt, Schema{{Name: "x", Type: TypeString}})
	second := WithDiscovered(opt, Schema{{Name: "y", Type: TypeString}})

	if got := first.Schema[len(first.Schema)-1].Name; got != "x" {
		t.Errorf("after a second call the first result's last column is %q: "+
			"the two share a backing array and one batch's column lands in "+
			"another's table", got)
	}
	if got := second.Schema[len(second.Schema)-1].Name; got != "y" {
		t.Errorf("the second result's last column is %q", got)
	}
	// And the original is untouched.
	if len(opt.Schema) != 2 || len(opt.Columns) != 2 {
		t.Errorf("the caller's declaration grew: %v / %v", opt.Columns, opt.Schema.Names())
	}
	if shared[:cap(shared)][2].Name != "" {
		t.Errorf("append wrote into the caller's backing array: %v", shared[:cap(shared)][2])
	}
}

// The extension carries both halves, and names the discovered columns so the
// row check can tell them apart.
func TestWithDiscoveredExtendsBothHalves(t *testing.T) {
	opt := WriteOptions{
		Columns: []string{"a"},
		Schema:  Schema{{Name: "a", Type: TypeString}},
	}
	got := WithDiscovered(opt, Schema{{Name: "z", Type: TypeJSON}})

	if len(got.Columns) != 2 || got.Columns[1] != "z" {
		t.Errorf("Columns = %v", got.Columns)
	}
	if len(got.Schema) != 2 || got.Schema[1].Name != "z" || got.Schema[1].Type != TypeJSON {
		t.Errorf("Schema = %v", got.Schema)
	}
	if len(got.Discovered) != 1 || got.Discovered[0] != "z" {
		t.Errorf("Discovered = %v -- the row check has to tell a column the "+
			"BATCH contributed from one the consumer declared", got.Discovered)
	}
	// Nothing found changes nothing.
	if same := WithDiscovered(opt, nil); len(same.Columns) != 1 || len(same.Discovered) != 0 {
		t.Errorf("an empty discovery changed the declaration: %v", same.Columns)
	}
}

// A discovered column may be absent from a record, and that is not an error.
//
// It is the difference between the two halves of CheckRow. "You declared X
// and your chain does not produce it" is a bug worth stopping for. "The batch
// carried X in record 50 and not in record 0" is what a landing table is: a
// record missing one writes NULL there.
func TestCheckRowLetsADiscoveredColumnBeAbsent(t *testing.T) {
	records := batch(map[string]any{"id": "A"})

	// Declared and absent: still refused, naming the column.
	err := CheckRow([]string{"id", "declarada"}, nil, records, nil)
	if err == nil || !strings.Contains(err.Error(), "declarada") {
		t.Fatalf("a declared column the row does not deliver was accepted: %v", err)
	}

	// Discovered and absent: fine.
	if err := CheckRow([]string{"id", "achada"}, nil, records, []string{"achada"}); err != nil {
		t.Errorf("a discovered column absent from this record was refused: %v\n\n"+
			"A batch holds N records and they need not carry the same fields. "+
			"The union is what the table has to have; a record missing one of "+
			"them writes NULL, which is what a landing table legitimately does.",
			err)
	}

	// And the other half still fires: an undeclared field is still refused.
	err = CheckRow([]string{"id"}, nil, batch(map[string]any{"id": "A", "nova": 1}), []string{"achada"})
	if err == nil || !strings.Contains(err.Error(), "nova") {
		t.Errorf("an undeclared field was accepted: %v", err)
	}
}
