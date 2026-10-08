package load

import (
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// The table as the consumer's own scratch case has it: BigQuery kept the
// spelling the CREATE used, and their producer sends the other one.
func theirTable() bigquery.Schema {
	return bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType},
		{Name: "createdAt", Type: bigquery.TimestampFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType},
	}
}

// S2 — their scratch case. The declaration says `createdat`, the table says
// `createdAt`, and BigQuery stores both under the same column: a load job
// writing `createdat` lands in `createdAt`, measured against a real project
// on 2026-10-08. So there is nothing absent and nothing to ALTER.
func TestADeclarationInAnotherCaseIsNotAbsentFromTheTable(t *testing.T) {
	err := checkDeclaredAgainstTable([]string{"id", "createdat"}, theirTable(), "ds.t")
	if err != nil {
		t.Errorf("a column the table HAS was reported absent: %v", err)
	}
}

// And the other half, which is what makes the check worth keeping: a column
// that genuinely is not there is still named.
func TestAColumnTheTableReallyLacksIsStillRefused(t *testing.T) {
	err := checkDeclaredAgainstTable([]string{"id", "nacionalidade"}, theirTable(), "ds.t")
	if err == nil {
		t.Fatal("a column no spelling of which is in the table was accepted")
	}
	if !strings.Contains(err.Error(), "nacionalidade") {
		t.Errorf("the refusal does not name it: %v", err)
	}
}

// S2 — the same question on the evolve path. `createdat` against `createdAt`
// used to be "missing", which produced an ADD COLUMN that BigQuery answers
// with 400 `Field createdat already exists in schema`.
func TestEvolvePlansNoAlterForAColumnTheTableHasInAnotherCase(t *testing.T) {
	declared := bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType},
		{Name: "createdat", Type: bigquery.TimestampFieldType},
	}
	if got := missingFrom(theirTable(), declared); len(got) != 0 {
		t.Errorf("planned an ALTER for %s; BigQuery answers 400 to it", namesOf(got))
	}
}

func TestEvolveStillAddsAColumnTheTableDoesNotHave(t *testing.T) {
	declared := bigquery.Schema{
		{Name: "createdat", Type: bigquery.TimestampFieldType},
		{Name: "nacionalidade", Type: bigquery.StringFieldType},
	}
	got := missingFrom(theirTable(), declared)
	if len(got) != 1 || got[0].Name != "nacionalidade" {
		t.Errorf("planned %s, wanted exactly nacionalidade", namesOf(got))
	}
}

// S3 — the MERGE's reconcile. The staging table is created from the
// DECLARATION, so it carries the declaration's spelling; the destination
// carries whatever the CREATE used. Compared by exact name, every merge into
// a table spelled the other way refuses with "the rows carry column(s)
// createdat, which ... does not have".
func TestTheMergeReconcilesAcrossSpellings(t *testing.T) {
	incoming := bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType},
		{Name: "createdat", Type: bigquery.TimestampFieldType},
	}
	cols, err := reconcile(theirTable(), incoming)
	if err != nil {
		t.Fatalf("the merge refused a staging table BigQuery would have accepted: %v", err)
	}
	// The DESTINATION's spelling, because that is the table the MERGE names.
	want := []string{"id", "createdAt"}
	if strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Errorf("merging %v, wanted %v", cols, want)
	}
}

// And the type check it also does is not weakened by the fold.
func TestTheMergeStillRefusesAnIncompatibleTypeAcrossSpellings(t *testing.T) {
	incoming := bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType},
		{Name: "CREATEDAT", Type: bigquery.StringFieldType},
	}
	_, err := reconcile(theirTable(), incoming)
	if err == nil || !strings.Contains(err.Error(), "CREATEDAT") {
		t.Errorf("a STRING into a TIMESTAMP column passed, or was not named: %v", err)
	}
}

// S3 — CheckDestination's own filter, the one that lets EvolveAdditive reach
// its only case. Before the fold it kept `createdat` out of `declared` for
// the wrong reason and the check below then never saw it either, so the two
// bugs cancelled. A mutation of one alone would have survived.
func TestCheckDestinationCountsAFoldedColumnAsPresent(t *testing.T) {
	kept := declaredThatTheTableHas([]string{"id", "createdat", "nacionalidade"}, theirTable())
	if strings.Join(kept, ",") != "id,createdat" {
		t.Errorf("kept %v, wanted the two the table has under some spelling", kept)
	}
}

// S3 — the gateway's union through the real encoder: two spellings in two
// records become one column, and NEITHER record's value is dropped on the
// way to the wire. BigQuery folds them into the one column on load.
func TestTheFlushEncodesBothSpellings(t *testing.T) {
	opt := core.WriteOptions{
		Columns:   []string{"id", "nationalID"},
		FoldsCase: true,
	}
	rows, err := EncodeRows([]core.Envelope{
		{Payload: map[string]any{"id": "1", "nationalID": "x"}},
		{Payload: map[string]any{"id": "2", "nationalId": "y"}},
	}, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"nationalID":"x"`, `"nationalId":"y"`} {
		if !strings.Contains(string(rows), want) {
			t.Errorf("the wire does not carry %s:\n%s", want, rows)
		}
	}
}
