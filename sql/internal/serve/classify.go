package serve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/sqltok"
)

// ReadOnly refuses any statement that is not a single query.
//
// THIS IS THE FUNCTION BETWEEN A BROWSER AND A CUSTOMER'S WAREHOUSE, and it
// is written to be read by somebody deciding whether to trust it.
//
// OVER A TOKENISER AND NOT A REGEX, which is the whole reason `sqltok`
// exists as its own package. A pattern matching `DELETE` cannot tell code
// from a string literal or a comment, so it refuses
// `WHERE note = 'please DELETE this row'` -- a legitimate query -- and it
// accepts `/* SELECT */ DROP TABLE t`. Both of those are cases below.
//
// WHAT IT CANNOT DO, said plainly so nobody discovers it as a bug: a
// function call writes whatever the function writes. `SELECT nextval('s')`
// advances a sequence and `SELECT dblink_exec(...)` runs anything at all,
// and no classifier that is not a planner can see inside them. THE READ-ONLY
// ROLE IS THE DEFENCE THERE, not this -- which is exactly the question
// CHECKPOINT B exists to answer before this is deployed anywhere.
//
// WHEN IT CANNOT FOLLOW THE STATEMENT, IT REFUSES. A parser that gives up
// and allows is a parser that can be made to give up on purpose.
func ReadOnly(dialect, statement string) error {
	toks := sqltok.Tokenize(dialect, statement)

	// A trailing `;` is punctuation. One anywhere else is a second
	// statement, whatever follows it -- `SELECT 1; SELECT 2` is refused too,
	// because "one statement" is the rule and not "one write".
	for len(toks) > 0 && toks[len(toks)-1].Is(";") {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return errors.New("there is no statement here to run")
	}
	for _, t := range toks {
		if t.Is(";") {
			return errors.New("this is more than one statement, and this service runs one")
		}
	}

	// The statement itself: SELECT, or a WITH whose every part is read.
	if err := query(toks, 0); err != nil {
		return err
	}

	for i, t := range toks {
		switch {
		// A CTE CLAUSE INSIDE A SUBQUERY. `SELECT * FROM (WITH x AS (…) …)`
		// is a second place a WITH can appear, and a data-modifying CTE
		// hides there just as well as at the front.
		//
		// Only where a statement can BEGIN, which is the front or just
		// inside a paren. `FROM unnest(x) WITH ORDINALITY` is the word in
		// another job, and walking it as a CTE list would refuse a query
		// that reads.
		case t.LowerIs("with") && (i == 0 || toks[i-1].Is("(")):
			if err := withClause(toks, i+1); err != nil {
				return err
			}

		// SELECT … INTO MAKES A TABLE in Postgres, and it is the one write
		// that wears a SELECT at the front. `INTO` has no read-only use.
		case t.LowerIs("into"):
			return errors.New("this service only reads, and INTO writes a table")

		// FOR UPDATE / FOR SHARE / FOR NO KEY UPDATE take row locks. Not a
		// write, and not a thing a glance at a destination should be able to
		// do to somebody's production table either.
		case t.LowerIs("for") && i+1 < len(toks):
			switch toks[i+1].Lower {
			case "update", "share", "no", "key":
				return errors.New("this service only reads, and FOR UPDATE takes a lock")
			}

		// THE BELT. Every position where a statement could begin that the
		// walk above did not visit -- a paren this did not understand, a
		// form neither dialect has yet. A write verb cannot be the head of
		// an expression, so there is nothing legitimate to catch here.
		case t.Is("(") && i+1 < len(toks) && writes[toks[i+1].Lower] && toks[i+1].Kind == sqltok.Word:
			return fmt.Errorf("this service only reads, and this holds a %s statement",
				strings.ToUpper(toks[i+1].Lower))
		}
	}
	return nil
}

// query checks the statement beginning at i, which is a SELECT or nothing.
func query(toks []sqltok.Token, i int) error {
	// `(SELECT 1) UNION ALL (SELECT 2)` begins with a paren and is a query.
	for i < len(toks) && toks[i].Is("(") {
		i++
	}
	if i >= len(toks) {
		return errors.New("there is no statement here to run")
	}
	switch {
	case toks[i].LowerIs("select"), toks[i].LowerIs("values"), toks[i].LowerIs("table"):
		return nil
	case toks[i].LowerIs("with"):
		return withClause(toks, i+1)
	}
	return fmt.Errorf("this service only reads, and %s", describe(toks[i]))
}

// withClause walks `[RECURSIVE] name [(cols)] AS [NOT] [MATERIALIZED] ( body )
// [, …]` and then the statement after the list.
//
// BOTH HALVES WRITE. Postgres takes `WITH x AS (DELETE … RETURNING *) SELECT
// …`, so a body has to be checked; and `WITH x AS (SELECT 1) DELETE FROM t`
// is a delete wearing a reading CTE, so what FOLLOWS the list has to be
// checked too. Missing either one is a write that passed a classifier.
func withClause(toks []sqltok.Token, i int) error {
	if i < len(toks) && toks[i].LowerIs("recursive") {
		i++
	}
	for {
		if i >= len(toks) || (toks[i].Kind != sqltok.Word && toks[i].Kind != sqltok.Quoted) {
			return cannotFollow()
		}
		i++
		if i < len(toks) && toks[i].Is("(") { // the column list
			i = skipParens(toks, i)
		}
		if i >= len(toks) || !toks[i].LowerIs("as") {
			return cannotFollow()
		}
		i++
		for i < len(toks) && (toks[i].LowerIs("not") || toks[i].LowerIs("materialized")) {
			i++
		}
		if i >= len(toks) || !toks[i].Is("(") {
			return cannotFollow()
		}
		if err := query(toks, i+1); err != nil {
			return err
		}
		i = skipParens(toks, i)
		if i < len(toks) && toks[i].Is(",") {
			i++
			continue
		}
		// What the whole WITH is FOR. A clause with nothing after it is not
		// a statement, and a warehouse would say so -- but it is also a
		// shape this cannot vouch for, and this does not guess.
		return query(toks, i)
	}
}

func cannotFollow() error {
	return errors.New("this service cannot follow this WITH clause, so it will not run it")
}

// skipParens returns the index just past the paren opening at i.
func skipParens(toks []sqltok.Token, i int) int {
	depth := 0
	for ; i < len(toks); i++ {
		switch {
		case toks[i].Is("("):
			depth++
		case toks[i].Is(")"):
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

// describe says what kind of statement this is, WITHOUT REPEATING IT.
//
// A refusal travels into a browser, a screenshot and a ticket. A keyword this
// service already knows is safe to name and is the thing somebody needs to
// read; anything else is somebody's own text and stays where they typed it.
func describe(t sqltok.Token) string {
	if t.Kind == sqltok.Word && (writes[t.Lower] || commands[t.Lower]) {
		return "this is a " + strings.ToUpper(t.Lower) + " statement"
	}
	return "this does not begin with SELECT"
}

// writes are verbs that change something. They are the head of a statement or
// of a CTE body and never the head of an expression, which is what makes
// finding one inside a paren conclusive.
var writes = set(`insert update delete merge truncate drop create alter grant
	revoke call execute declare export begin commit rollback vacuum refresh
	reindex prepare discard attach detach replace upsert`)

// commands are the rest of what a statement can begin with: not writes, but
// not queries either, and worth naming in a refusal rather than meeting the
// generic sentence.
var commands = set(`explain analyze copy load set reset show use describe
	listen notify unlisten lock cluster comment do pragma fetch close deallocate
	checkpoint`)

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}
