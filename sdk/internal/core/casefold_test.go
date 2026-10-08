package core

import (
	"strings"
	"testing"
)

// THE REPORTER'S FLUSH. [#43]
//
// One flush for `id_workspace_blacklist` held three events from one producer:
// two DELETEs carrying `nationalID`/`workspaceID` and an INSERT carrying
// `nationalId`/`workspaceId`. No single record carried both — they met in a
// batch. The schema derived from it had both, and BigQuery refused the CREATE
// with `Field nationalId already exists in schema`.
//
// BigQuery folds column case. MEASURED against a real project on 2026-10-08,
// through a load job and not only through DML: a row written with `createdat`
// into a table whose column is `createdAt` lands in `createdAt`.
func TestABatchWithTwoSpellingsDeclaresOneColumn(t *testing.T) {
	deletes := Envelope{Payload: map[string]any{"id": "1", "nationalID": "x", "workspaceID": "w"}}
	insert := Envelope{Payload: map[string]any{"id": "2", "nationalId": "y", "workspaceId": "v"}}

	// BOTH ORDERS, and they must agree. `nationalID` sorts BEFORE
	// `nationalId` ('D' is 0x44, 'd' is 0x64), so in one of these two the
	// winner by sorted order is NOT the one that arrived first -- which is
	// the whole point. A column name is DDL: the same batch replayed must
	// not create a differently-spelled column.
	for _, order := range []struct {
		name string
		in   []Envelope
	}{
		{"the flush as it arrived", []Envelope{deletes, insert}},
		{"the same flush, replayed", []Envelope{insert, deletes}},
	} {
		got, err := Discovered(WriteOptions{Columns: []string{"id"}, FoldsCase: true}, order.in)
		if err != nil {
			t.Fatalf("%s: discovering: %v", order.name, err)
		}
		var names []string
		for _, c := range got {
			names = append(names, c.Name)
		}
		if len(names) != 2 {
			t.Fatalf("%s: declared %v, wanted one column per folded name", order.name, names)
		}
		// The first in SORTED order, which is the rule FlattenOneLevel
		// already uses and for the reason it gives.
		if names[0] != "nationalID" || names[1] != "workspaceID" {
			t.Errorf("%s: declared %v, wanted nationalID, workspaceID", order.name, names)
		}
	}
}

// Postgres and MySQL keep quoted identifiers distinct, so two spellings are
// two columns there and this must not reach them.
func TestWithoutFoldingTwoSpellingsStayTwoColumns(t *testing.T) {
	records := []Envelope{
		{Payload: map[string]any{"id": "1", "nationalID": "x"}},
		{Payload: map[string]any{"id": "2", "nationalId": "y"}},
	}
	got, err := Discovered(WriteOptions{Columns: []string{"id"}}, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("a destination that does not fold got %d columns, wanted 2", len(got))
	}
}

// A declaration the consumer already made is not folded away either: the
// column exists, in their table, and this rule is about what the SDK CREATES.
func TestAFoldedMatchAgainstTheDeclarationDeclaresNothing(t *testing.T) {
	records := []Envelope{{Payload: map[string]any{"id": "1", "CreatedAt": "t"}}}
	got, err := Discovered(WriteOptions{
		Columns: []string{"id", "createdat"}, FoldsCase: true,
	}, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("declared %+v, and `createdat` is already the consumer's column", got)
	}
}

// ONE RECORD CARRYING BOTH is the one real conflict, and it is refused by
// name -- the shape the flattening refusal already uses.
func TestOneRecordCarryingBothSpellingsIsRefused(t *testing.T) {
	records := []Envelope{
		{Payload: map[string]any{"id": "1", "nationalID": "a", "nationalId": "b"}},
	}
	_, err := Discovered(WriteOptions{Columns: []string{"id"}, FoldsCase: true}, records)
	if err == nil {
		t.Fatal("a record carrying both spellings was accepted")
	}
	for _, want := range []string{"nationalID", "nationalId"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n  %v", want, err)
		}
	}
}

// And NOT refused: two records each carrying one spelling. That is their
// actual traffic -- 1,340 INSERTs one way against 23 DELETE/UPDATEs the
// other -- and refusing it would dead-letter what this fix exists to land.
func TestTwoRecordsEachWithOneSpellingAreFine(t *testing.T) {
	records := []Envelope{
		{Payload: map[string]any{"id": "1", "nationalID": "a"}},
		{Payload: map[string]any{"id": "2", "nationalId": "b"}},
	}
	if _, err := Discovered(WriteOptions{Columns: []string{"id"}, FoldsCase: true}, records); err != nil {
		t.Errorf("their actual traffic was refused: %v", err)
	}
}

// S3 — the JSON check, which is the one where a miss is SILENT. A string in
// a JSON column stores a JSON value of type string: JSON_TYPE returns
// "string", every JSON_VALUE against it returns NULL, and BigQuery reports
// no error. Asked by exact name, a row carrying `payload` against a declared
// `Payload` walked straight past it.
func TestAJSONColumnIsCheckedUnderEitherSpelling(t *testing.T) {
	declared := Schema{
		{Name: "id", Type: TypeString},
		{Name: "Payload", Type: TypeJSON},
	}
	records := []Envelope{
		{Payload: map[string]any{"id": "1", "payload": `{"a":1}`}},
	}
	err := CheckJSONColumns(WriteOptions{Schema: declared, FoldsCase: true}, records)
	if err == nil {
		t.Fatal("a string reached a JSON column: it stores as a JSON value " +
			"of type string, and nothing anywhere reports an error")
	}
	// Named with the DECLARATION's spelling, which is what the consumer edits.
	if !strings.Contains(err.Error(), `"Payload"`) {
		t.Errorf("the refusal names something other than the declared column: %v", err)
	}
}

// And on a destination that does not fold, `payload` is simply a field the
// declaration does not have -- CheckRow's business, not this one's.
func TestWithoutFoldingTheJSONCheckStaysOnTheExactName(t *testing.T) {
	declared := Schema{{Name: "Payload", Type: TypeJSON}}
	records := []Envelope{{Payload: map[string]any{"payload": `{"a":1}`}}}
	if err := CheckJSONColumns(WriteOptions{Schema: declared}, records); err != nil {
		t.Errorf("a field that is not the declared column was judged as it: %v", err)
	}
}

// S3 — the verdict that says NOT to fold, pinned. [#43]
//
// CheckNormalizeRenames fires exactly when the table has `createdAt` and the
// load declares `createdat`: it exists to say that BREVIS_NORMALIZE_DATA
// would lower-case the column and leave the old one behind with the rows
// already in it. Fold the comparison and the two names are equal, so it
// never fires -- a check that cannot fail. This test is what makes that
// verdict a decision rather than an oversight.
func TestNormalizeRenamesMustNotFoldItsComparison(t *testing.T) {
	inTable := map[string]ColumnType{"createdAt": TypeString}
	err := checkNormalizeRenames(true, []string{"createdat"}, inTable, "ds.t")
	if err == nil {
		t.Fatal("the rename check was folded away. It is ABOUT case: with " +
			"both spellings treated as one name it can never fire again, " +
			"and a consumer turning the flag on loses the column silently")
	}
	for _, want := range []string{"createdAt", "createdat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}
