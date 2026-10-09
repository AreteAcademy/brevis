// Package refs answers one question about a query: which relations does it
// READ. That is all dependency inference needs, and it is deliberately less
// than a parser knows.
//
// WHY NOT A PARSER. The spike behind #58 measured five, against official
// parsers as the oracle. memefish parsed 6 of 447 real BigQuery statements;
// go-pgquery is the Postgres oracle and weighs 14 MB with 70 packages;
// cockroachdb-parser is 37 MB and does not build without cgo. This reads
// 99.3% of 1,543 fresh Postgres statements and 26 of 26 fresh BigQuery, in
// 1.9 MB with nothing outside the standard library.
//
// WHAT IT CANNOT DO, so nobody discovers it as a bug: `(TABLE x)` forms,
// uncommon clause keywords that take a relation-looking name, and anything
// that builds its SQL at runtime. A model that hits one declares the edge
// with `depends_on:` in its header, which is why that field exists.
//
// The thirty cases in testdata were labelled by hand BEFORE any candidate
// ran, and the extractor was written afterwards -- so passing them is not
// evidence that it works, it is the floor that says it has not regressed.
// The evidence is in spikes/sql-parser/VERDICT.md, against SQL nobody chose.
package refs

import (
	"sort"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/sqltok"
)

// Of lists the relations a query reads, in the order they appear.
func Of(dialect, sql string) ([]string, error) {
	// THE TRUNCATION FLAG IS DROPPED HERE ON PURPOSE, and this is the one
	// caller that may. This package infers EDGES from a model somebody wrote
	// and committed; a model whose SQL does not close a quote is one the
	// warehouse refuses on the next build, and a missing edge in a graph is
	// not a security boundary. `serve` is where it matters, because there
	// the statement arrives from a browser.
	toks, _ := sqltok.Tokenize(dialect, sql)
	if dialect == "postgres" {
		// Postgres folds an unquoted identifier to lower case: POINT_TBL and
		// point_tbl are one table. Added AFTER the held-out run showed 754 of
		// 824 disagreements were exactly this -- a rule of the language, not a
		// fit to the data, and reported beside the number it changed.
		for i := range toks {
			if toks[i].Kind == sqltok.Word {
				toks[i].Text = toks[i].Lower
			}
		}
	}
	ctes := map[string]bool{}
	aliases := map[string]bool{}
	exclude := map[string]bool{}
	var refs []string

	// Paren stack: the word that opened each paren, so FROM inside
	// EXTRACT(... FROM ...) or SUBSTRING(... FROM ...) is not a FROM clause.
	var opener []string

	// fromList[depth] is true while a FROM list is open at that paren depth.
	fromList := map[int]bool{}

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.Is("("):
			prev := ""
			if i > 0 && toks[i-1].Kind == sqltok.Word {
				prev = toks[i-1].Lower
			}
			opener = append(opener, prev)
			// A parenthesised join -- FROM (a JOIN b) or JOIN (TABLE x) --
			// opens with a relation and no FROM before it.
			if prev == "from" || prev == "join" {
				j := i + 1
				if j < len(toks) && toks[j].LowerIs("table") {
					j++
				}
				if j < len(toks) && !toks[j].LowerIs("select") && !toks[j].LowerIs("with") && !toks[j].LowerIs("values") {
					if name, next, ok := readName(toks, j); ok && !isCall(toks, next) {
						refs = append(refs, name)
						fromList[len(opener)] = true
						i = next - 1
					}
				}
			}
			continue
		case t.Is(")"):
			delete(fromList, len(opener))
			if len(opener) > 0 {
				opener = opener[:len(opener)-1]
			}
			continue
		case t.Is(","):
			// A comma at the depth of an open FROM list starts its next item,
			// whatever came before it: a table, a subquery, a JOIN ... ON (...).
			if fromList[len(opener)] {
				i = readFromItems(toks, i+1, false, &refs, aliases)
			}
			continue
		}
		if t.Kind != sqltok.Word {
			continue
		}
		switch t.Lower {
		case "with":
			i = readCTEs(toks, i+1, ctes)
		case "merge", "update":
			// MERGE [INTO] target / UPDATE target: written, not read.
			j := i + 1
			if j < len(toks) && toks[j].LowerIs("into") {
				j++
			}
			if name, next, ok := readName(toks, j); ok {
				exclude[name] = true
				i = next - 1
			}
		case "insert":
			j := i + 1
			if j < len(toks) && toks[j].LowerIs("into") {
				if name, next, ok := readName(toks, j+1); ok {
					exclude[name] = true
					i = next - 1
				}
			}
		case "from", "join", "using":
			if t.Lower == "from" && insideFunctionFrom(opener) {
				continue
			}
			if t.Lower == "from" && i >= 2 && toks[i-1].LowerIs("distinct") &&
				(toks[i-2].LowerIs("is") || toks[i-2].LowerIs("not")) {
				continue
			}
			// CYCLE col SET mark [TO v DEFAULT d] USING path: a column.
			if t.Lower == "using" && cycleBefore(toks, i) {
				continue
			}
			if t.Lower == "from" {
				fromList[len(opener)] = true
			}
			i = readFromItems(toks, i+1, false, &refs, aliases)
		case "where", "group", "having", "qualify", "window", "order", "limit",
			"union", "intersect", "except", "returning", "set", "pivot", "unpivot":
			delete(fromList, len(opener))
		}
	}

	// A path whose head is an alias is a correlated array (`FROM e, e.items`).
	var kept []string
	for _, r := range refs {
		if head, _, dotted := strings.Cut(r, "."); dotted && aliases[strings.ToLower(head)] {
			continue
		}
		kept = append(kept, r)
	}
	return finish(kept, ctes, exclude), nil
}

// insideFunctionFrom reports whether the innermost paren was opened by a
// function whose syntax uses FROM as an argument separator.
func insideFunctionFrom(opener []string) bool {
	if len(opener) == 0 {
		return false
	}
	switch opener[len(opener)-1] {
	case "extract", "substring", "trim", "overlay", "position":
		return true
	}
	return false
}

// readCTEs reads `[RECURSIVE] name [(cols)] AS [NOT] [MATERIALIZED] ( ... ) [, ...]`.
func readCTEs(toks []sqltok.Token, i int, ctes map[string]bool) int {
	if i < len(toks) && toks[i].LowerIs("recursive") {
		i++
	}
	// ONE CTE, and the rest by recursion below -- not a loop. Every path out
	// of this block returns, so a `for` here reads as iteration that never
	// happens: the second CTE is reached through readCTEs calling itself at
	// the comma, and the body of the first is scanned by the caller.
	if i < len(toks) {
		if toks[i].Kind != sqltok.Word && toks[i].Kind != sqltok.Quoted {
			return i - 1
		}
		name := toks[i].Text
		j := i + 1
		if j < len(toks) && toks[j].Is("(") {
			j = skipParens(toks, j)
		}
		if j >= len(toks) || !toks[j].LowerIs("as") {
			return i - 1
		}
		ctes[strings.ToLower(name)] = true
		j++
		for j < len(toks) && (toks[j].LowerIs("not") || toks[j].LowerIs("materialized")) {
			j++
		}
		if j >= len(toks) || !toks[j].Is("(") {
			return j - 1
		}
		// The body is scanned by the caller like any other tokens: return
		// to just inside it, after noting where the next CTE could begin.
		end := skipParens(toks, j)
		if end < len(toks) && toks[end].Is(",") {
			// Record the later names now; their bodies are scanned in order.
			readCTEs(toks, end+1, ctes)
		}
		return j - 1
	}
	return i - 1
}

// readFromItems reads one FROM/JOIN/USING item -- or, after FROM, a comma
// list of them -- recording table names and their aliases. Subqueries,
// table functions and parenthesised column lists are left to the main scan.
func readFromItems(toks []sqltok.Token, i int, list bool, refs *[]string, aliases map[string]bool) int {
	for i < len(toks) {
		for i < len(toks) && (toks[i].LowerIs("lateral") || toks[i].LowerIs("only")) {
			i++
		}
		if i >= len(toks) || toks[i].Is("(") {
			return i - 1 // a subquery or a USING (cols) list
		}
		name, next, ok := readName(toks, i)
		if !ok {
			return i - 1
		}
		if isCall(toks, next) {
			return i - 1
		}
		*refs = append(*refs, name)
		i = next
		if i < len(toks) && toks[i].LowerIs("as") {
			i++
		}
		if i < len(toks) && (toks[i].Kind == sqltok.Word || toks[i].Kind == sqltok.Quoted) && !keywords[toks[i].Lower] {
			aliases[strings.ToLower(toks[i].Text)] = true
			i++
		}
		if !list || i >= len(toks) || !toks[i].Is(",") {
			return i - 1
		}
		i++
	}
	return i - 1
}

// isCall says whether the name ending at i is followed by an open paren --
// `unnest()`, `generate_series()`, a table function. A call is not a relation
// and must not become an edge, which is the one thing this answer decides.
//
// Named because it is asked in two places and was spelt out in both: once as
// the negated half of a condition in readWith's VALUES/SELECT branch, once
// with a comment in readFromItems explaining what it meant. Two spellings of
// one rule is two places for it to drift.
func isCall(toks []sqltok.Token, i int) bool {
	return i < len(toks) && toks[i].Is("(")
}

// readName reads a dotted name. A backticked BigQuery name may hold dots of
// its own; it is kept as written.
func readName(toks []sqltok.Token, i int) (string, int, bool) {
	if i >= len(toks) || (toks[i].Kind != sqltok.Word && toks[i].Kind != sqltok.Quoted) || (toks[i].Kind == sqltok.Word && keywords[toks[i].Lower]) {
		return "", i, false
	}
	parts := []string{toks[i].Text}
	i++
	for i+1 < len(toks) && toks[i].Is(".") && (toks[i+1].Kind == sqltok.Word || toks[i+1].Kind == sqltok.Quoted) {
		parts = append(parts, toks[i+1].Text)
		i += 2
	}
	// BigQuery wildcard: `events_*` unquoted is `events_` then `*`.
	if i < len(toks) && toks[i].Is("*") && len(parts) > 1 {
		parts[len(parts)-1] += "*"
		i++
	}
	return strings.Join(parts, "."), i, true
}

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

// keywords that may follow a table name and must not be read as its alias.
var keywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`select from where join left right inner outer full
		cross natural on using group order by limit offset union intersect except all
		qualify pivot unpivot window having lateral tablesample with as when then
		set values returning for fetch matched not and or into merge update insert
		delete default`) {
		keywords[k] = true
	}
}

// cycleBefore reports whether a CYCLE clause opened within the last few
// tokens: its USING names a path column, not a relation.
func cycleBefore(toks []sqltok.Token, i int) bool {
	for j := i - 1; j >= 0 && j >= i-12; j-- {
		if toks[j].LowerIs("cycle") {
			return true
		}
	}
	return false
}

// finish drops CTE names (unqualified only), excluded names and duplicates,
// and sorts.
//
// A CTE is dropped only when it is UNQUALIFIED: `with orders as (...)` hides
// the bare name `orders` and says nothing about `raw.orders`, which is a
// different relation that a query may well read in the same statement.
func finish(refs []string, ctes, exclude map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range refs {
		if r == "" || exclude[r] || seen[r] {
			continue
		}
		if !strings.Contains(r, ".") && ctes[strings.ToLower(r)] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
