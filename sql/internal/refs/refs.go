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
)

// Of lists the relations a query reads, in the order they appear.
func Of(dialect, sql string) ([]string, error) {
	toks := tokenize(dialect, sql)
	if dialect == "postgres" {
		// Postgres folds an unquoted identifier to lower case: POINT_TBL and
		// point_tbl are one table. Added AFTER the held-out run showed 754 of
		// 824 disagreements were exactly this -- a rule of the language, not a
		// fit to the data, and reported beside the number it changed.
		for i := range toks {
			if toks[i].kind == word {
				toks[i].text = toks[i].lower
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
		case t.is("("):
			prev := ""
			if i > 0 && toks[i-1].kind == word {
				prev = toks[i-1].lower
			}
			opener = append(opener, prev)
			// A parenthesised join -- FROM (a JOIN b) or JOIN (TABLE x) --
			// opens with a relation and no FROM before it.
			if prev == "from" || prev == "join" {
				j := i + 1
				if j < len(toks) && toks[j].lowerIs("table") {
					j++
				}
				if j < len(toks) && !toks[j].lowerIs("select") && !toks[j].lowerIs("with") && !toks[j].lowerIs("values") {
					if name, next, ok := readName(toks, j); ok && !isCall(toks, next) {
						refs = append(refs, name)
						fromList[len(opener)] = true
						i = next - 1
					}
				}
			}
			continue
		case t.is(")"):
			delete(fromList, len(opener))
			if len(opener) > 0 {
				opener = opener[:len(opener)-1]
			}
			continue
		case t.is(","):
			// A comma at the depth of an open FROM list starts its next item,
			// whatever came before it: a table, a subquery, a JOIN ... ON (...).
			if fromList[len(opener)] {
				i = readFromItems(toks, i+1, false, &refs, aliases)
			}
			continue
		}
		if t.kind != word {
			continue
		}
		switch t.lower {
		case "with":
			i = readCTEs(toks, i+1, ctes)
		case "merge", "update":
			// MERGE [INTO] target / UPDATE target: written, not read.
			j := i + 1
			if j < len(toks) && toks[j].lowerIs("into") {
				j++
			}
			if name, next, ok := readName(toks, j); ok {
				exclude[name] = true
				i = next - 1
			}
		case "insert":
			j := i + 1
			if j < len(toks) && toks[j].lowerIs("into") {
				if name, next, ok := readName(toks, j+1); ok {
					exclude[name] = true
					i = next - 1
				}
			}
		case "from", "join", "using":
			if t.lower == "from" && insideFunctionFrom(opener) {
				continue
			}
			if t.lower == "from" && i >= 2 && toks[i-1].lowerIs("distinct") &&
				(toks[i-2].lowerIs("is") || toks[i-2].lowerIs("not")) {
				continue
			}
			// CYCLE col SET mark [TO v DEFAULT d] USING path: a column.
			if t.lower == "using" && cycleBefore(toks, i) {
				continue
			}
			if t.lower == "from" {
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
func readCTEs(toks []tok, i int, ctes map[string]bool) int {
	if i < len(toks) && toks[i].lowerIs("recursive") {
		i++
	}
	// ONE CTE, and the rest by recursion below -- not a loop. Every path out
	// of this block returns, so a `for` here reads as iteration that never
	// happens: the second CTE is reached through readCTEs calling itself at
	// the comma, and the body of the first is scanned by the caller.
	if i < len(toks) {
		if toks[i].kind != word && toks[i].kind != quoted {
			return i - 1
		}
		name := toks[i].text
		j := i + 1
		if j < len(toks) && toks[j].is("(") {
			j = skipParens(toks, j)
		}
		if j >= len(toks) || !toks[j].lowerIs("as") {
			return i - 1
		}
		ctes[strings.ToLower(name)] = true
		j++
		for j < len(toks) && (toks[j].lowerIs("not") || toks[j].lowerIs("materialized")) {
			j++
		}
		if j >= len(toks) || !toks[j].is("(") {
			return j - 1
		}
		// The body is scanned by the caller like any other tokens: return
		// to just inside it, after noting where the next CTE could begin.
		end := skipParens(toks, j)
		if end < len(toks) && toks[end].is(",") {
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
func readFromItems(toks []tok, i int, list bool, refs *[]string, aliases map[string]bool) int {
	for i < len(toks) {
		for i < len(toks) && (toks[i].lowerIs("lateral") || toks[i].lowerIs("only")) {
			i++
		}
		if i >= len(toks) || toks[i].is("(") {
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
		if i < len(toks) && toks[i].lowerIs("as") {
			i++
		}
		if i < len(toks) && (toks[i].kind == word || toks[i].kind == quoted) && !keywords[toks[i].lower] {
			aliases[strings.ToLower(toks[i].text)] = true
			i++
		}
		if !list || i >= len(toks) || !toks[i].is(",") {
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
func isCall(toks []tok, i int) bool {
	return i < len(toks) && toks[i].is("(")
}

// readName reads a dotted name. A backticked BigQuery name may hold dots of
// its own; it is kept as written.
func readName(toks []tok, i int) (string, int, bool) {
	if i >= len(toks) || (toks[i].kind != word && toks[i].kind != quoted) || (toks[i].kind == word && keywords[toks[i].lower]) {
		return "", i, false
	}
	parts := []string{toks[i].text}
	i++
	for i+1 < len(toks) && toks[i].is(".") && (toks[i+1].kind == word || toks[i+1].kind == quoted) {
		parts = append(parts, toks[i+1].text)
		i += 2
	}
	// BigQuery wildcard: `events_*` unquoted is `events_` then `*`.
	if i < len(toks) && toks[i].is("*") && len(parts) > 1 {
		parts[len(parts)-1] += "*"
		i++
	}
	return strings.Join(parts, "."), i, true
}

func skipParens(toks []tok, i int) int {
	depth := 0
	for ; i < len(toks); i++ {
		switch {
		case toks[i].is("("):
			depth++
		case toks[i].is(")"):
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

type kind int

const (
	word kind = iota
	quoted
	punct
	literal
)

type tok struct {
	kind  kind
	text  string // as written; a quoted identifier without its quotes
	lower string
}

func (t tok) is(p string) bool      { return t.kind == punct && t.text == p }
func (t tok) lowerIs(w string) bool { return t.kind == word && t.lower == w }

// tokenize drops comments and string literals and splits the rest. `#` starts
// a comment in BigQuery only; in Postgres it is an operator.
func tokenize(dialect, s string) []tok {
	var out []tok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-', c == '#' && dialect == "bigquery":
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return out
			}
			i += end + 4
		case c == '\'' || (c == '"' && dialect == "bigquery"):
			// A string. In BigQuery "..." is a string too.
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && dialect == "bigquery" {
					j += 2
					continue
				}
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			out = append(out, tok{kind: literal})
			i = j + 1
		case c == '"' || c == '`':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				return out
			}
			text := s[i+1 : i+1+j]
			out = append(out, tok{kind: quoted, text: text, lower: strings.ToLower(text)})
			i += j + 2
		case isWordByte(c):
			j := i
			for j < len(s) && (isWordByte(s[j]) || (s[j] == '-' && dialect == "bigquery" && j > i && j+1 < len(s) && isWordByte(s[j+1]) && !isDigit(s[i]))) {
				j++
			}
			text := s[i:j]
			out = append(out, tok{kind: word, text: text, lower: strings.ToLower(text)})
			i = j
		default:
			out = append(out, tok{kind: punct, text: string(c)})
			i++
		}
	}
	return out
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c == '@' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || isDigit(c)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// cycleBefore reports whether a CYCLE clause opened within the last few
// tokens: its USING names a path column, not a relation.
func cycleBefore(toks []tok, i int) bool {
	for j := i - 1; j >= 0 && j >= i-12; j-- {
		if toks[j].lowerIs("cycle") {
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
