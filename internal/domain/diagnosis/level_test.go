package diagnosis

import "testing"

// EVERY LINE IS AN ERROR, A WARNING OR NEITHER, by what real tools write.
// Each row is a line a step in this ecosystem actually prints.
func TestALinesLevelIsWhatTheToolMeantByIt(t *testing.T) {
	for line, want := range map[string]Level{
		// Errors.
		`panic: runtime error: index out of range [3] with length 3`:                 Err,
		`ValueError: invalid literal for int() with base 10: 'x'`:                    Err,
		`Traceback (most recent call last):`:                                         Err,
		`Error: connect ECONNREFUSED 127.0.0.1:5432`:                                 Err,
		`TypeError: Cannot read properties of undefined (reading 'id')`:              Err,
		`ERROR: relation "orders" does not exist`:                                    Err,
		`2026-10-28 03:12:01 ERROR loading batch 3`:                                  Err,
		`FAIL 1 not_null_alerts_id ............................. [FAIL 14 in 0.12s]`: Err,
		`Failure in test not_null_alerts_id (models/schema.yml)`:                     Err,
		`fatal: could not read Username for 'https://github.com'`:                    Err,
		`context deadline exceeded`:                                                  Err,
		`Get "https://api.ipma.pt/x": context deadline exceeded (Client.Timeout)`:    Err,
		`exit status 1`: Err,
		`level=ERROR msg="writing the batch" error="permission denied"`: Err,
		// Warnings.
		`WARN  retrying in 2s`:                                             Warn,
		`WARNING: the column "x" is deprecated`:                            Warn,
		`level=WARN msg="slow page" ms=2400`:                               Warn,
		`/app/x.py:3: DeprecationWarning: datetime.utcnow() is deprecated`: Warn,
		`warning: unused variable`:                                         Warn,
		// Neither.
		`fetched 1,209 rows from https://api.example.com/orders`: Info,
		`level=INFO msg="landed" rows=1209`:                      Info,
		`OK created sql table model analytics.orders`:            Info,
		`Done. PASS=4 WARN=0 ERROR=0 SKIP=0 TOTAL=4`:             Info,
		``: Info,
	} {
		if got := LevelOf(line); got != want {
			t.Errorf("LevelOf(%q) = %s, want %s", line, got, want)
		}
	}
}

// A HEURISTIC, AND IT IS WRONG SOMETIMES -- written down here so nobody
// "fixes" it into something worse. A word is not a meaning: a table called
// report that starts by counting errors reads as one, even when the count is zero. The screen shows the line with its
// neighbours, which is what makes a false alarm cheap.
func TestAKnownFalseAlarmIsDocumented(t *testing.T) {
	if got := LevelOf(`ERROR count for the day: 0 (every row loaded)`); got != Err {
		t.Skipf("the false alarm no longer fires (%s); remove this test and the note", got)
	}
}

// THE FIRST ERROR is where "Go to the error" lands when the cause did not
// come from the log: the line where things started to go wrong, counted
// from 1 like an editor does.
func TestFirstErrorIsTheFirstErrorLineCountedFromOne(t *testing.T) {
	log := "starting\nWARN slow\nfetched 3 rows\nError: boom\nmore\npanic: again\n"
	n, ok := FirstError(log)
	if !ok || n != 4 {
		t.Errorf("FirstError = %d, %v; want 4", n, ok)
	}
	if _, ok := FirstError("all\nfine\n"); ok {
		t.Error("a clean log has a first error")
	}
	if _, ok := FirstError(""); ok {
		t.Error("an empty log has a first error")
	}
}
