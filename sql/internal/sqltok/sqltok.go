// Package sqltok splits SQL into tokens and does nothing else with them.
//
// IT HAS TWO CALLERS AND THAT IS WHY IT EXISTS. `refs` asks which relations a
// query reads; `serve` asks whether a statement only reads. Both questions
// turn on the same thing -- telling CODE from a string literal, a quoted
// identifier and a comment -- and both are dialect-specific in the same
// places: `#` starts a comment in BigQuery and is an operator in Postgres,
// `"..."` is a string in BigQuery and an identifier in Postgres.
//
// Those rules in two copies would be two copies that drift, and the one that
// drifts is a classifier deciding that `/* SELECT */ DROP TABLE t` is a
// SELECT. One lexer, one place to be wrong, one place to fix.
//
// IT IS NOT A PARSER, deliberately. The measurement behind that choice is in
// refs's own doc comment: the official parsers weigh 14 to 37 MB and one of
// them read 6 of 447 real statements. This is 1.9 MB of standard library, and
// what it cannot do is the reason its callers refuse rather than guess.
package sqltok

import "strings"

// Kind is what a token IS, which is the only classification this package
// makes. A Literal keeps no text: nothing downstream may read inside a string
// a customer wrote, and the surest way to guarantee that is to not carry it.
type Kind int

const (
	Word Kind = iota
	Quoted
	Punct
	Literal
)

// Token is one piece of a statement.
type Token struct {
	Kind  Kind
	Text  string // as written; a quoted identifier without its quotes
	Lower string
}

// Is reports whether this is a particular piece of punctuation.
func (t Token) Is(p string) bool { return t.Kind == Punct && t.Text == p }

// LowerIs reports whether this is a particular bare word, in any case.
func (t Token) LowerIs(w string) bool { return t.Kind == Word && t.Lower == w }

// Tokenize drops comments and the contents of string literals and splits the
// rest. `#` starts a comment in BigQuery only; in Postgres it is an operator.
//
// THE SECOND RETURN SAYS WHETHER IT REACHED THE END, and no caller may ignore
// it by accident. An unterminated quote, backtick or block comment leaves
// this with no way to know where code resumes, so it stops -- and a caller
// that took the tokens alone would be judging a PREFIX. CHECKPOINT B found
// exactly that: `SELECT 1 /* ; DROP TABLE t` read as `SELECT 1`.
//
// It was not exploitable, because a statement that truncates this also fails
// to parse at the warehouse. But that is two lexers agreeing rather than a
// decision anybody made, and the classifier's own rule is that what it cannot
// follow, it refuses.
func Tokenize(dialect, s string) (toks []Token, whole bool) {
	var out []Token
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
				return out, false
			}
			i += end + 4
		case c == '\'' || (c == '"' && dialect == "bigquery"):
			// A string. In BigQuery "..." is a string too.
			j, closed := i+1, false
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
					closed = true
					break
				}
				j++
			}
			out = append(out, Token{Kind: Literal})
			// A string that never closes is the one case that used to pass
			// silently: the loop simply ran out and this looked like a
			// complete literal.
			if !closed {
				return out, false
			}
			i = j + 1
		case c == '"' || c == '`':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				return out, false
			}
			text := s[i+1 : i+1+j]
			out = append(out, Token{Kind: Quoted, Text: text, Lower: strings.ToLower(text)})
			i += j + 2
		case isWordByte(c):
			j := i
			for j < len(s) && (isWordByte(s[j]) || (s[j] == '-' && dialect == "bigquery" && j > i && j+1 < len(s) && isWordByte(s[j+1]) && !isDigit(s[i]))) {
				j++
			}
			text := s[i:j]
			out = append(out, Token{Kind: Word, Text: text, Lower: strings.ToLower(text)})
			i = j
		default:
			out = append(out, Token{Kind: Punct, Text: string(c)})
			i++
		}
	}
	return out, true
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c == '@' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || isDigit(c)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
