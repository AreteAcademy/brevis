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
