package core

import (
	"strings"
	"testing"
)

// "Nil throughout" is a question about the BATCH, and answering it per record
// turns a refusal into silent data loss.
//
// A field nil in the first record and carrying a value in a later one is a
// real column. If nothing declared it and no mode discovers it, CheckRow has
// to stop the load by name -- that value would otherwise be written nowhere
// and nobody would be told.
//
// A mutation answering it per record survived every other test, because every
// other case had EvolveAdditiveFromPayload on, which declares the column and
// takes it out of this half of the check entirely.
func TestANilInTheFirstRecordIsNotAnEmptyField(t *testing.T) {
	records := []Envelope{
		{Payload: map[string]any{"id": "A", "valor": nil}},
		{Payload: map[string]any{"id": "B", "valor": "8.89"}},
	}

	err := CheckRow([]string{"id"}, Schema{{Name: "id", Type: TypeString}}, records, nil)
	if err == nil {
		t.Fatal("a field with a VALUE that nothing declares was let through: " +
			"no column exists for it, so it is written nowhere -- and the " +
			"whole point of this half of the check is to say so rather than " +
			"drop it quietly")
	}
	if !strings.Contains(err.Error(), "valor") {
		t.Errorf("the refusal is %q and does not name the field", err)
	}
}

// And the same batch with no value anywhere is let through, because there is
// nothing to write.
func TestAFieldNilInEveryRecordIsNotAnExtra(t *testing.T) {
	records := []Envelope{
		{Payload: map[string]any{"id": "A", "valor": nil}},
		{Payload: map[string]any{"id": "B", "valor": nil}},
	}
	if err := CheckRow([]string{"id"},
		Schema{{Name: "id", Type: TypeString}}, records, nil); err != nil {
		t.Errorf("a field null in every record stopped the load: nothing "+
			"declares it, nothing creates it, and nothing writes it -- a null "+
			"and an absent field land the same NULL: %v", err)
	}
}

// RowFields keeps a nil-throughout field the table HAS, and leaves out the one
// it does not.
func TestRowFieldsKeepsWhatTheTableHas(t *testing.T) {
	records := []Envelope{
		{Payload: map[string]any{"id": "A", "declared_empty": nil, "unknown_empty": nil}},
	}
	got := RowFields(records, []string{"id", "declared_empty"})
	want := "declared_empty,id"
	if strings.Join(got, ",") != want {
		t.Errorf("RowFields = %v, want [%s].\n\n`declared_empty` has a column, "+
			"so NULL is a value for it and a MERGE that leaves it out keeps "+
			"the value the producer just cleared. `unknown_empty` has no "+
			"column, so there is nothing to write.", got, want)
	}
}
