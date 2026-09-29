package core

import (
	"strings"
	"testing"
	"time"
)

// A column the DATA created says so, where somebody meets it.
//
// postgres.go already says why this matters and has said it for a while:
// "a change to a table's shape that happens in silence is a change nobody can
// date afterwards, and 'when did this column appear' is the question
// consumers ask months later". Until now the answer was a log line, which
// rotates.
func TestADiscoveredColumnCarriesItsOrigin(t *testing.T) {
	found, err := Discovered([]string{"id"}, batch(map[string]any{"id": "A", "series": "21129"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("%d discovered", len(found))
	}
	note := found[0].Note
	if note == "" {
		t.Fatal("a column that came from a batch carries no note: six months " +
			"from now nothing in the table says nobody declared it")
	}
	if !strings.Contains(note, time.Now().UTC().Format("2006-01-02")) {
		t.Errorf("the note does not date the column: %q", note)
	}
	if !strings.Contains(note, "batch") {
		t.Errorf("the note does not say where the column came from: %q", note)
	}
}

// And the refusal names it, which is the moment somebody most needs to know.
//
// The drift case: a field that was scalar on Monday made a text column, and
// on Tuesday it arrives as an object. The plan refuses -- rightly, it is a
// change of kind -- and the message used to read as though somebody had
// declared that column and got it wrong. Nobody did.
func TestTheRefusalSaysAColumnCameFromABatch(t *testing.T) {
	declared := Schema{
		{Name: "id", Type: TypeString},
		{Name: "valor", Type: TypeJSON, Note: "brevis: added from a batch on 2026-09-28"},
	}
	_, err := declared.Plan(
		map[string]ColumnType{"id": TypeString, "valor": TypeString},
		EvolveAdditiveFromPayload, "bronze.bacen")
	if err == nil {
		t.Fatal("a change of kind was accepted")
	}
	if !strings.Contains(err.Error(), "valor") {
		t.Errorf("the refusal does not name the column: %v", err)
	}
	if !strings.Contains(err.Error(), "batch") {
		t.Errorf("the refusal reads as though somebody declared this column "+
			"and got it wrong. Nobody did -- a batch created it, and that "+
			"changes what the reader should do: %v", err)
	}

	// A column the consumer DID declare keeps the message it always had.
	_, err = Schema{{Name: "valor", Type: TypeJSON}}.Plan(
		map[string]ColumnType{"valor": TypeString}, EvolveAdditive, "t")
	if err == nil {
		t.Fatal("a change of kind was accepted")
	}
	if strings.Contains(err.Error(), "batch") {
		t.Errorf("a column the consumer declared was blamed on a batch: %v", err)
	}
}

// The note reaches the DDL, so the table itself carries the answer.
func TestTheNoteReachesTheAlter(t *testing.T) {
	s := Schema{{Name: "series", Type: TypeString, Note: "brevis: from a batch"}}
	changes := []Change{{Column: "series", Kind: "add", To: TypeString}}

	for _, d := range []Dialect{Postgres, MySQL} {
		stmts, err := s.AlterTable(d, "bronze.bacen", changes)
		if err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		var said bool
		for _, sql := range stmts {
			if strings.Contains(sql, "from a batch") {
				said = true
			}
		}
		if !said {
			t.Errorf("%s: the ADD does not carry the note, so the column "+
				"exists with nothing saying where it came from:\n  %s",
				d.Name, strings.Join(stmts, "\n  "))
		}
	}

	// A column with no note renders exactly as before.
	plain := Schema{{Name: "series", Type: TypeString}}
	stmts, err := plain.AlterTable(Postgres, "t", changes)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 1 {
		t.Errorf("an unnoted column rendered %d statements: %v", len(stmts), stmts)
	}
}

// A quote in a note must not end the SQL string.
//
// TWO WRONG TESTS CAME BEFORE THIS ONE, and both are worth recording because
// they failed in opposite directions.
//
// The first looked for the substring `'; DROP` and FAILED ON CORRECT OUTPUT:
// doubling turns `'; DROP` into `''; DROP`, which still contains it.
// Pattern-matching the attack was the wrong question.
//
// The second stripped the outer quotes and undoubled, expecting the original
// back -- and PASSED WITH THE ESCAPING REMOVED, because undoubling a string
// that was never doubled is a no-op that matches. A check that cannot fail.
//
// The property is where the literal ENDS. Walk from the opening quote: a
// single quote must always be followed by another, or the string is over. If
// it is over before the statement is, everything after it is SQL.
func TestANoteCannotEscapeItsQuotes(t *testing.T) {
	const note = "it's '; DROP TABLE t; --"
	s := Schema{{Name: "c", Type: TypeString, Note: note}}
	stmts, err := s.AlterTable(Postgres, "t", []Change{{Column: "c", Kind: "add", To: TypeString}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("%d statements, want the ADD and its comment: %v", len(stmts), stmts)
	}

	comment := stmts[1]
	open := strings.Index(comment, "'")
	if open < 0 {
		t.Fatalf("the comment has no literal: %s", comment)
	}

	i := open + 1
	for i < len(comment) {
		if comment[i] != '\'' {
			i++
			continue
		}
		if i+1 < len(comment) && comment[i+1] == '\'' {
			i += 2 // a doubled quote, still inside the string
			continue
		}
		break // unpaired: the literal ends here
	}
	if i != len(comment)-1 {
		t.Errorf("the literal ends at %d of %d, and %q is SQL:\n  %s\n\n"+
			"DDL takes no parameters -- an ALTER is text or it is nothing -- "+
			"so a note that ends its own string turns the rest of it into "+
			"statements.", i, len(comment)-1, comment[i+1:], comment)
	}

	// And it round-trips, which is the same property from the other side --
	// on its own it proves nothing, because undoubling a string that was
	// never doubled also matches.
	if got := strings.ReplaceAll(comment[open+1:len(comment)-1], "''", "'"); got != note {
		t.Errorf("the literal does not round-trip:\n  in  %q\n  out %q", note, got)
	}
}
