package core

import (
	"strings"
	"testing"
)

// A Schema IS a declaration, and the discovery used to say it was not.
//
// From the audit after #42's third round. `Discovered` refuses the payload
// mode when nothing is declared -- rightly, because completing nothing would
// mean creating the whole table from a payload, which this SDK does not do.
// It read `Columns` to decide, and a caller who declares with a `Schema` and
// no `Columns` has declared plenty:
//
//	this destination is set to evolve from the payload and nothing is declared
//
// That is false, and it names the one thing they did do. The gateway escapes
// it only because its router sends EvolveAdditive rather than the payload
// mode; a driver used directly with a Schema does not.
func TestASchemaIsADeclarationForTheDiscovery(t *testing.T) {
	schema := Schema{{Name: "id", Type: TypeString}}

	got, err := Discovered(WriteOptions{Schema: schema}, []Envelope{
		{Payload: map[string]any{"id": "A-1", "novo": "x"}},
	})
	if err != nil {
		t.Fatalf("a Schema-only declaration was refused:\n\n%v", err)
	}
	if len(got) != 1 || got[0].Name != "novo" {
		t.Fatalf("discovered %v, want just `novo`", got.Names())
	}
	// And `id` is NOT rediscovered: the Schema already declares it, so the
	// column is the consumer's and not the batch's. Reading Columns alone
	// would have made every declared field look new.
	for _, c := range got {
		if c.Name == "id" {
			t.Error("a column the Schema declares was reported as discovered: " +
				"it would be added to the table a second time, and dated as " +
				"though a batch had brought it")
		}
	}
}

// And with neither, the refusal stands. It is the whole point of the mode.
func TestWithNoDeclarationAtAllTheDiscoveryStillRefuses(t *testing.T) {
	_, err := Discovered(WriteOptions{}, []Envelope{
		{Payload: map[string]any{"id": "A-1"}},
	})
	if err == nil {
		t.Fatal("a payload-mode load with nothing declared was allowed: it " +
			"would create the whole table from the payload")
	}
	if !strings.Contains(err.Error(), "nothing is declared") {
		t.Errorf("the refusal is %q", err)
	}
}

// DeclaredColumns gives the same answer on both carriers, because it is the
// same question. Two accessors with one rule, rather than one rule written
// twice.
func TestBothCarriersAnswerTheSameWay(t *testing.T) {
	schema := Schema{{Name: "a", Type: TypeString}, {Name: "b", Type: TypeString}}

	for _, tc := range []struct {
		name    string
		columns []string
		schema  Schema
		want    string
	}{
		{"schema only", nil, schema, "a,b"},
		{"columns only", []string{"x", "y"}, nil, "x,y"},
		{"neither", nil, nil, ""},
		// Both is refused by sdk.Target before it gets here; if one ever
		// arrives, Columns is the explicit list and wins.
		{"both", []string{"x"}, schema, "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := LoadConfig{Columns: tc.columns, Schema: tc.schema}
			opt := WriteOptions{Columns: tc.columns, Schema: tc.schema}
			if got := strings.Join(cfg.DeclaredColumns(), ","); got != tc.want {
				t.Errorf("LoadConfig gives %q, want %q", got, tc.want)
			}
			if got := strings.Join(opt.DeclaredColumns(), ","); got != tc.want {
				t.Errorf("WriteOptions gives %q, want %q", got, tc.want)
			}
		})
	}
}

// WithDiscovered extends the DECLARATION, not the Columns slice.
//
// Caught by running the PUBLISHED v0.79.0 against the gateway's config shape,
// which is the only reason it was caught at all: the SDK's own tests declare
// Columns, so `opt.Columns` is never the empty slice there.
//
// With a Schema and no Columns, extending `opt.Columns` produces a Columns
// list holding ONLY the discovered names -- and CheckRow, which reads Columns,
// then refuses the row for carrying every column the Schema declared:
//
//	the row carries brevis_ingestion_id, brevis_operation, …, which Columns
//	does not declare
//
// The declaration is what grows. Columns was never meant to end up smaller
// than what the caller declared.
func TestWithDiscoveredExtendsTheWholeDeclaration(t *testing.T) {
	schema := Schema{
		{Name: "brevis_ingestion_id", Type: TypeString},
		{Name: "id", Type: TypeString},
	}
	found := Schema{{Name: "novo", Type: TypeString}}

	got := WithDiscovered(WriteOptions{Schema: schema}, found)

	want := "brevis_ingestion_id,id,novo"
	if g := strings.Join(got.Columns, ","); g != want {
		t.Errorf("Columns = %q, want %q.\n\nA Schema-only caller ends up with "+
			"a Columns list that names only what the BATCH brought, and "+
			"CheckRow then refuses the row for carrying what the caller "+
			"declared.", g, want)
	}
	if g := strings.Join(got.Schema.Names(), ","); g != want {
		t.Errorf("Schema = %q, want %q", g, want)
	}
	if g := strings.Join(got.Discovered, ","); g != "novo" {
		t.Errorf("Discovered = %q, want just the batch's own", g)
	}
}

// A Columns-only caller is unchanged.
func TestWithDiscoveredLeavesAColumnsCallerAlone(t *testing.T) {
	got := WithDiscovered(
		WriteOptions{Columns: []string{"id"}},
		Schema{{Name: "novo", Type: TypeString}})
	if g := strings.Join(got.Columns, ","); g != "id,novo" {
		t.Errorf("Columns = %q, want id,novo", g)
	}
}
