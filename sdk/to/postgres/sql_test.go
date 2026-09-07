package postgres

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestInsertSQLNamesTheColumns: §5.4 of the plan asks for the SQL asserted as a
// pure function, with no client -- and the reason is concrete. BigQuery's MERGE
// shipped with a POSITIONAL match and cost v0.12.0 precisely because the SQL was
// built inside a method that held a client and had never been seen by a
// teste.
func TestInsertSQLNamesTheColumns(t *testing.T) {
	got := InsertSQL("landing.pedidos", "brevis_stage",
		[]string{"ingestion_id", "provider", "valor"})

	esperado := `INSERT INTO landing.pedidos ("ingestion_id", "provider", "valor") ` +
		`SELECT "ingestion_id", "provider", "valor" FROM brevis_stage ` +
		`ON CONFLICT ("ingestion_id") DO NOTHING`
	if got != esperado {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, esperado)
	}
}

// TestInsertSQLQuotesIdentifiers: a column called "order" or "select" is
// legitimate, and unquoted it becomes a syntax error in the middle of a load --
// on a batch that has already run the whole extract.
func TestInsertSQLQuotesIdentifiers(t *testing.T) {
	got := InsertSQL("t", "s", []string{core.MetadataID, "order", "group"})
	for _, palavra := range []string{`"order"`, `"group"`} {
		if !strings.Contains(got, palavra) {
			t.Errorf("%s nao esta citado:\n%s", palavra, got)
		}
	}
}

// TestInsertSQLEscapesQuotes: a quote inside the name would close the identifier
// and the rest of the column would become SQL.
func TestInsertSQLEscapesQuotes(t *testing.T) {
	got := InsertSQL("t", "s", []string{`a"b`})
	if !strings.Contains(got, `"a""b"`) {
		t.Errorf("aspa nao escapada:\n%s", got)
	}
}

// TestInsertSQLConflictsOnIngestionID: the SDK's dedup is on ingestion_id, and
// swapping that column for another would make the dedup match on the wrong
// thing in
// silencio.
func TestInsertSQLConflictsOnIngestionID(t *testing.T) {
	got := InsertSQL("t", "s", []string{"a"})
	if !strings.Contains(got, `ON CONFLICT ("`+core.MetadataID+`")`) {
		t.Errorf("o ON CONFLICT nao e por %s:\n%s", core.MetadataID, got)
	}
}

// TestSplitName covers the implicit schema and a name with too many parts.
func TestSplitName(t *testing.T) {
	casos := []struct {
		name, esquema, table string
		failure              bool
	}{
		{"pedidos", "public", "pedidos", false},
		{"landing.pedidos", "landing", "pedidos", false},
		{"a.b.c", "", "", true},
	}
	for _, c := range casos {
		e, tb, err := splitName(c.name)
		if (err != nil) != c.failure {
			t.Errorf("%q: erro = %v, esperado erro=%v", c.name, err, c.failure)
			continue
		}
		if !c.failure && (e != c.esquema || tb != c.table) {
			t.Errorf("%q = (%q, %q), esperado (%q, %q)", c.name, e, tb, c.esquema, c.table)
		}
	}
}

// TestRowsFollowTheTablesOrder: COPY FROM matches by POSITION. If the rows came
// out in the record's order, every value would land in the wrong column -- and
// the database would accept it quietly whenever the types happened to line
// up.
func TestRowsFollowTheTablesOrder(t *testing.T) {
	envelopes := []core.Envelope{
		{Payload: map[string]any{"c": 3, "a": 1, "b": 2}},
		{Payload: map[string]any{"b": 20, "a": 10}}, // no "c"
	}
	l := &rows{columns: []string{"a", "b", "c"}, envelopes: envelopes}

	if !l.Next() {
		t.Fatal("sem primeira linha")
	}
	v, err := l.Values()
	if err != nil {
		t.Fatal(err)
	}
	if v[0] != 1 || v[1] != 2 || v[2] != 3 {
		t.Errorf("linha 1 = %v, esperado [1 2 3] na ordem da TABELA", v)
	}

	if !l.Next() {
		t.Fatal("sem segunda linha")
	}
	v, err = l.Values()
	if err != nil {
		t.Fatal(err)
	}
	// A column the record does not carry becomes NULL, which is legitimate in a
	// landing table.
	if v[0] != 10 || v[1] != 20 || v[2] != nil {
		t.Errorf("linha 2 = %v, esperado [10 20 <nil>]", v)
	}

	if l.Next() {
		t.Error("Next continuou depois do fim")
	}
}

// TestRowsDoesNotAllocatePerRow: a batch of 500 thousand records must not
// allocate a new slice per row. pgx consumes each row before asking for the
// next, so the buffer is reusable -- and this test pins that.
func TestRowsDoesNotAllocatePerRow(t *testing.T) {
	envelopes := make([]core.Envelope, 1000)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{"a": i}}
	}
	l := &rows{columns: []string{"a"}, envelopes: envelopes}

	allocations := testing.AllocsPerRun(100, func() {
		l.i = 0
		for l.Next() {
			if _, err := l.Values(); err != nil {
				t.Fatal(err)
			}
		}
	})
	// Zero is the target: the buffer is allocated once, outside the measured
	// loop.
	if allocations > 0 {
		t.Errorf("%.0f alocacoes para 1000 rows; o buffer deveria ser reusado", allocations)
	}
}
