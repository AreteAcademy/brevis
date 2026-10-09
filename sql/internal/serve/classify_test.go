package serve

import (
	"strings"
	"testing"
)

// A TABLE OF STATEMENTS, EACH WITH ITS VERDICT. This is the one function in
// brevis-sql that stands between a browser and a customer's warehouse, so it
// is tested the way the thing it guards deserves: every case somebody would
// actually try, and every case somebody would try ON PURPOSE.
//
// The cases that matter most are the ones a regex gets wrong, and they are
// marked. A literal holding the word DELETE is a SELECT; a comment holding
// the word SELECT is not.
func TestWhatOnlyReadsAndWhatDoesNot(t *testing.T) {
	cases := []struct {
		name    string
		dialect string
		sql     string
		ok      bool
	}{
		// --- reads ---
		{"the simplest query", "postgres", "SELECT 1", true},
		{"a table", "bigquery", "SELECT * FROM `acme.bronze.orders` LIMIT 10", true},
		{"lower case", "postgres", "select a, b from bronze.orders where a > 1", true},
		{"a trailing semicolon is punctuation", "postgres", "SELECT 1;", true},
		{"a trailing semicolon and space", "postgres", "SELECT 1 ;  \n", true},
		{"a CTE", "postgres", "WITH recent AS (SELECT * FROM t) SELECT * FROM recent", true},
		{"two CTEs", "postgres",
			"WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a JOIN b ON true", true},
		{"a recursive CTE", "postgres",
			"WITH RECURSIVE t AS (SELECT 1 UNION ALL SELECT n+1 FROM t) SELECT * FROM t", true},
		{"a materialized CTE", "postgres",
			"WITH a AS NOT MATERIALIZED (SELECT 1) SELECT * FROM a", true},
		{"a CTE with a column list", "postgres",
			"WITH a (x) AS (SELECT 1) SELECT * FROM a", true},
		{"a nested CTE inside a subquery", "postgres",
			"SELECT * FROM (WITH x AS (SELECT 1) SELECT * FROM x) s", true},
		{"a parenthesised union", "postgres", "(SELECT 1) UNION ALL (SELECT 2)", true},
		{"a VALUES list", "postgres", "VALUES (1), (2)", true},
		{"a leading comment", "postgres", "-- what this is for\nSELECT 1", true},
		{"a block comment first", "postgres", "/* a note */ SELECT 1", true},
		{"WITH ORDINALITY is not a CTE", "postgres",
			"SELECT * FROM generate_series(1, 3) WITH ORDINALITY AS t(v, n)", true},

		// --- the cases a regex gets wrong ---
		{"DELETE inside a string literal", "postgres",
			"SELECT * FROM notes WHERE body = 'please DELETE this row'", true},
		{"DROP inside a BigQuery double-quoted string", "bigquery",
			`SELECT * FROM t WHERE s = "DROP TABLE x"`, true},
		{"a column named delete, quoted", "postgres", `SELECT "delete" FROM t`, true},
		{"DROP after a comment that holds SELECT", "postgres",
			"/* SELECT */ DROP TABLE t", false},
		{"DROP after a line comment that holds SELECT", "bigquery",
			"# SELECT\nDROP TABLE t", false},

		// --- writes ---
		{"delete", "postgres", "DELETE FROM bronze.orders", false},
		{"update", "postgres", "UPDATE t SET a = 1", false},
		{"insert", "postgres", "INSERT INTO t SELECT * FROM s", false},
		{"drop", "postgres", "DROP TABLE t", false},
		{"create", "bigquery", "CREATE OR REPLACE VIEW v AS SELECT 1", false},
		{"truncate", "postgres", "TRUNCATE t", false},
		{"grant", "postgres", "GRANT SELECT ON t TO analyst", false},
		{"merge", "bigquery", "MERGE INTO t USING s ON t.k = s.k WHEN MATCHED THEN DELETE", false},
		{"call", "postgres", "CALL do_the_thing()", false},
		{"bigquery scripting", "bigquery", "DECLARE x INT64 DEFAULT 1", false},
		{"export data", "bigquery", "EXPORT DATA OPTIONS(uri='gs://b/*') AS SELECT 1", false},

		// --- the ones that look like a read ---
		{"a second statement", "postgres", "SELECT 1; DROP TABLE t", false},
		{"two reads are still two", "postgres", "SELECT 1; SELECT 2", false},
		{"a writing CTE", "postgres",
			"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x", false},
		{"a write AFTER a reading CTE", "postgres",
			"WITH x AS (SELECT 1) DELETE FROM t", false},
		{"an insert after a reading CTE", "postgres",
			"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", false},
		{"a writing CTE inside a subquery", "postgres",
			"SELECT * FROM (WITH x AS (UPDATE t SET a = 1 RETURNING *) SELECT * FROM x) s", false},
		// A CTE BODY IS AN ALLOW-LIST AND NOT A DENY-LIST, and these two say
		// so: EXPLAIN is not a write, so the paren belt below never sees it.
		// Only "a body is a query or it is refused" catches them.
		{"a CTE body that is not a query", "postgres",
			"WITH x AS (EXPLAIN SELECT 1) SELECT * FROM x", false},
		{"a nested CTE body that is not a query", "postgres",
			"SELECT * FROM (WITH x AS (EXPLAIN SELECT 1) SELECT * FROM x) s", false},
		{"SELECT INTO makes a table", "postgres", "SELECT * INTO new_t FROM old_t", false},
		{"FOR UPDATE takes a lock", "postgres", "SELECT * FROM t FOR UPDATE", false},
		{"FOR NO KEY UPDATE too", "postgres", "SELECT * FROM t FOR NO KEY UPDATE", false},

		// --- nothing to run ---
		{"empty", "postgres", "", false},
		{"only space", "postgres", "   \n\t ", false},
		{"only a comment", "postgres", "-- nothing here", false},
		{"only a semicolon", "postgres", ";", false},

		// --- a WITH this cannot follow is a WITH it refuses ---
		{"a WITH with no AS", "postgres", "WITH x (SELECT 1) SELECT * FROM x", false},
		{"a WITH that stops", "postgres", "WITH x AS (SELECT 1)", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ReadOnly(c.dialect, c.sql)
			if c.ok && err != nil {
				t.Fatalf("refused a query that only reads: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("accepted a statement that is not a single read")
			}
		})
	}
}

// A REFUSAL NAMES WHAT IT SAW, because the person reading it is the person
// who typed the SQL and "invalid statement" sends them to guess.
func TestARefusalSaysWhichKindOfStatementItWas(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"DELETE FROM t", "DELETE"},
		{"UPDATE t SET a = 1", "UPDATE"},
		{"CREATE TABLE t (a INT)", "CREATE"},
		{"SELECT 1; SELECT 2", "more than one statement"},
	} {
		err := ReadOnly("postgres", c.sql)
		if err == nil {
			t.Fatalf("%q was accepted", c.sql)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("refusing %q said %q, which never says %q", c.sql, err, c.want)
		}
	}
}

// THE STATEMENT IS NOT ECHOED BACK. A refusal travels into a browser, into a
// screenshot and into a ticket; a WHERE clause carries customer data, and the
// identifier somebody mistyped is not worth putting in all three.
//
// The same rule ParseTarget already follows, for the same reason.
func TestARefusalNeverEchoesTheStatement(t *testing.T) {
	const secret = "cpf_12345678900"
	for _, sql := range []string{
		"DELETE FROM t WHERE k = '" + secret + "'",
		"UPDATE " + secret + " SET a = 1",
		"SELECT 1; DROP TABLE " + secret,
		secret + " please",
	} {
		err := ReadOnly("postgres", sql)
		if err == nil {
			t.Fatalf("%q was accepted", sql)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal repeated the statement: %v", err)
		}
	}
}
