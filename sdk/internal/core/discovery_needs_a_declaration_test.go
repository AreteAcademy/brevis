package core

import (
	"strings"
	"testing"
)

// The mode COMPLETES a declaration. With nothing to complete it would become
// "create the whole table from the batch", which is a different decision with
// a different name.
//
// Found by probing rather than by reading: with no Schema and no Columns, a
// MySQL table came out `a longtext, b json` and nothing was refused. It walks
// straight past CreationPlan's careful "the SDK does not infer" message.
//
// The harm is concrete, and it is the landing layout. The row carries
// brevis_received_at, so it would be created as text instead of a timestamp;
// and brevis_loaded_at is ABSENT from the row -- it is a database DEFAULT --
// so it would not be created at all. The end-to-end latency measurement would
// be gone, in a table that looks right.
func TestDiscoveryNeedsADeclaration(t *testing.T) {
	err := CheckDiscoveryHasADeclaration(EvolveAdditiveFromPayload, nil, "bronze.bacen")
	if err == nil {
		t.Fatal("the mode was accepted with nothing declared: the SDK would " +
			"create the whole table from the payload")
	}
	for _, want := range []string{"bronze.bacen", "Schema"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// One declared column is a declaration. How MUCH is declared is the
	// consumer's business; that there IS one is the SDK's.
	if err := CheckDiscoveryHasADeclaration(
		EvolveAdditiveFromPayload, []string{"brevis_ingestion_id"}, "t"); err != nil {
		t.Errorf("a declaration of one column was refused: %v", err)
	}

	// And the other modes are untouched. A Target that declares nothing
	// declares nothing and checks nothing, which is the rule the rest of the
	// struct follows.
	for _, mode := range []Evolution{EvolveNone, EvolveAdditive} {
		if err := CheckDiscoveryHasADeclaration(mode, nil, "t"); err != nil {
			t.Errorf("%v with no declaration was refused: %v", mode, err)
		}
	}
}

// And Discovered refuses it too, because not every caller goes through
// CheckDestination: the drivers are exported and the gateway calls Write
// directly.
func TestDiscoveredRefusesAnEmptyDeclaration(t *testing.T) {
	_, err := Discovered(nil, batch(map[string]any{"a": "x"}))
	if err == nil {
		t.Fatal("Discovered accepted an empty declaration, so a caller that " +
			"skips CheckDestination still creates a table from the payload")
	}
}
