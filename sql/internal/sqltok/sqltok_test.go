package sqltok

import "testing"

// THE DIALECT CHANGES WHAT A CHARACTER IS, and these are the three places it
// does. Both callers depend on all three, which is the argument for one lexer.
func TestTheSameCharacterIsTwoThingsInTwoDialects(t *testing.T) {
	for _, c := range []struct {
		name    string
		dialect string
		sql     string
		want    []Token
	}{
		{"# is a comment in BigQuery", "bigquery", "# DROP TABLE t\nSELECT 1", []Token{
			{Kind: Word, Text: "SELECT", Lower: "select"},
			{Kind: Word, Text: "1", Lower: "1"},
		}},
		{"# is an operator in Postgres", "postgres", "a # b", []Token{
			{Kind: Word, Text: "a", Lower: "a"},
			{Kind: Punct, Text: "#"},
			{Kind: Word, Text: "b", Lower: "b"},
		}},
		{`" is a string in BigQuery`, "bigquery", `x = "DROP"`, []Token{
			{Kind: Word, Text: "x", Lower: "x"},
			{Kind: Punct, Text: "="},
			{Kind: Literal},
		}},
		{`" is an identifier in Postgres`, "postgres", `x = "DROP"`, []Token{
			{Kind: Word, Text: "x", Lower: "x"},
			{Kind: Punct, Text: "="},
			{Kind: Quoted, Text: "DROP", Lower: "drop"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Tokenize(c.dialect, c.sql)
			if len(got) != len(c.want) {
				t.Fatalf("%d tokens, wanted %d: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("token %d is %+v, wanted %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// A LITERAL KEEPS NO TEXT. Nothing downstream may read inside a string a
// customer wrote, and a field that is never filled is the only guarantee of
// that which cannot be forgotten by the next caller.
func TestAStringLiteralCarriesNothingOutOfTheLexer(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t WHERE cpf = '12345678900'",
		`SELECT * FROM t WHERE cpf = "12345678900"`,
		"SELECT * FROM t WHERE note = 'it''s quoted'",
	} {
		for _, d := range []string{"postgres", "bigquery"} {
			for _, tok := range Tokenize(d, sql) {
				if tok.Kind == Literal && (tok.Text != "" || tok.Lower != "") {
					t.Errorf("%s carried %q out of a literal", d, tok.Text)
				}
			}
		}
	}
}
