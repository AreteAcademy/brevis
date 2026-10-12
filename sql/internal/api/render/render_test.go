package render

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

func mustParse(t *testing.T, dia, src string) *Template {
	t.Helper()
	tmpl, err := Parse(dia, src)
	if err != nil {
		t.Fatalf("parsing %q: %v", src, err)
	}
	return tmpl
}

func TestTextWithNoTagsIsTheSQLAsWritten(t *testing.T) {
	src := "select day, sum(clicks)\nfrom marts.clicks_daily group by 1"
	sql, args, err := mustParse(t, "postgres", src).Render(nil, postgres.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	if sql != src || len(args) != 0 {
		t.Errorf("got %q with %v", sql, args)
	}
}

// A VALUE IS A PLACEHOLDER AND AN ARGUMENT, never text. Positional on
// Postgres, named on BigQuery, and in both the value appears nowhere in the
// SQL.
func TestAValueBecomesAPlaceholderAndAnArgument(t *testing.T) {
	src := "select * from t where w = {{ tenant }} and c = {{ campaign }}"
	vals := Values{"tenant": Bind("acme"), "campaign": Bind("c-9")}

	sql, args, err := mustParse(t, "postgres", src).Render(vals, postgres.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "select * from t where w = $1 and c = $2"; sql != want {
		t.Errorf("postgres SQL is %q, wanted %q", sql, want)
	}
	want := []dialect.Arg{{Name: "tenant", Value: "acme"}, {Name: "campaign", Value: "c-9"}}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args are %v, wanted %v", args, want)
	}

	sql, _, err = mustParse(t, "bigquery", src).Render(vals, bigquery.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "select * from t where w = @tenant and c = @campaign"; sql != want {
		t.Errorf("bigquery SQL is %q, wanted %q", sql, want)
	}
}

// ONE NAME, ONE ARGUMENT. `$1` twice is the same value twice, which is what
// the author wrote; two arguments would be a second chance to disagree.
func TestANameUsedTwiceIsBoundOnce(t *testing.T) {
	src := "select {{ d }} as a, {{ d }} as b"
	sql, args, err := mustParse(t, "postgres", src).Render(Values{"d": Bind(int64(1))}, postgres.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	if sql != "select $1 as a, $1 as b" || len(args) != 1 {
		t.Errorf("got %q with %v", sql, args)
	}
}

// AN ARGUMENT EXISTS ONLY IF ITS PLACEHOLDER WAS WRITTEN. A branch not taken
// binds nothing, or Postgres is sent a `$2` that no `$2` in the text uses
// and refuses the statement.
func TestConditionsChooseTheTextAndOnlyItsArguments(t *testing.T) {
	src := "select 1 where t = {{ tenant }}" +
		"{% if campaign %} and c = {{ campaign }}" +
		"{% elif region and not legacy %} and r = {{ region }}" +
		"{% else %} and true{% endif %}"
	tmpl := mustParse(t, "postgres", src)

	cases := []struct {
		vals Values
		sql  string
		args int
	}{
		{Values{"tenant": Bind("a"), "campaign": Bind("c")}, "select 1 where t = $1 and c = $2", 2},
		{Values{"tenant": Bind("a"), "region": Bind("eu"), "legacy": Bind(false)}, "select 1 where t = $1 and r = $2", 2},
		{Values{"tenant": Bind("a"), "region": Bind("eu"), "legacy": Bind(true)}, "select 1 where t = $1 and true", 1},
		{Values{"tenant": Bind("a")}, "select 1 where t = $1 and true", 1},
	}
	for _, c := range cases {
		sql, args, err := tmpl.Render(c.vals, postgres.Dialect{})
		if err != nil {
			t.Fatal(err)
		}
		if sql != c.sql || len(args) != c.args {
			t.Errorf("with %v got %q and %d args, wanted %q and %d", c.vals, sql, len(args), c.sql, c.args)
		}
	}
}

func TestOrBindsLooserThanAnd(t *testing.T) {
	tmpl := mustParse(t, "postgres", "{% if a or b and c %}yes{% else %}no{% endif %}")
	sql, _, _ := tmpl.Render(Values{"a": Bind(true), "b": Bind(false), "c": Bind(false)}, postgres.Dialect{})
	if sql != "yes" {
		t.Errorf("a or (b and c) with a true rendered %q", sql)
	}
	sql, _, _ = tmpl.Render(Values{"b": Bind(true)}, postgres.Dialect{})
	if sql != "no" {
		t.Errorf("a or (b and c) with only b true rendered %q", sql)
	}
}

// AN ENUM IS THE ONE VALUE THAT IS WRITTEN INTO THE SQL, because `order by
// day $1` is not SQL. It is safe only because the value is an identifier,
// and the renderer checks that again rather than trusting the caller did.
func TestAnInlineValueIsWrittenOnlyWhenItIsAnIdentifier(t *testing.T) {
	tmpl := mustParse(t, "postgres", "select 1 order by day {{ order }}")
	sql, args, err := tmpl.Render(Values{"order": Inline("desc")}, postgres.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	if sql != "select 1 order by day desc" || len(args) != 0 {
		t.Errorf("got %q with %v", sql, args)
	}
	for _, bad := range []string{"desc; drop table t", "desc --", "", "1desc", "de sc", "dé"} {
		if _, _, err := tmpl.Render(Values{"order": Inline(bad)}, postgres.Dialect{}); err == nil {
			t.Errorf("inline %q was written into the SQL", bad)
		}
	}
}

func TestAValueWithNothingToBindIsAnError(t *testing.T) {
	_, _, err := mustParse(t, "postgres", "select {{ campaign }}").Render(Values{}, postgres.Dialect{})
	if err == nil || !strings.Contains(err.Error(), "campaign") {
		t.Errorf("a missing value rendered, or the error does not name it: %v", err)
	}
}

// EVERYTHING OUTSIDE THE SUBSET IS A PARSE ERROR, and the error says what
// was found. The subset is the safety: there is no filter to mark a value
// safe, no attribute to reach into, no literal to splice.
func TestEverythingOutsideTheSubsetIsRefused(t *testing.T) {
	refused := map[string]string{
		"{{ x | safe }}":                "x | safe",
		"{{ x.y }}":                     "x.y",
		"{{ x[0] }}":                    "x[0]",
		"{{ 'lit' }}":                   "'lit'",
		"{{ }}":                         "empty",
		"{{ x y }}":                     "x y",
		"{% set x = 1 %}":               "set",
		"{% for x in y %}{% endfor %}":  "for",
		"{% include 'a' %}":             "include",
		"{% macro m() %}{% endmacro %}": "macro",
		"{%- if x %}{% endif %}":        "whitespace",
		"{{- x }}":                      "whitespace",
		"select {{ x":                   "never closed",
		"select {% if x %}":             "endif",
		"select {% endif %}":            "endif",
		"{% if x %}{% else %}{% else %}{% endif %}":   "else",
		"{% if x %}{% else %}{% elif y %}{% endif %}": "elif",
		"{% if %}{% endif %}":                         "condition",
		"{% if x and %}{% endif %}":                   "condition",
		"{% if (x) %}{% endif %}":                     "(x)",
		"{% if x == 1 %}{% endif %}":                  "==",
		"{# never closed":                             "never closed",
	}
	for src, says := range refused {
		_, err := Parse("postgres", src)
		if err == nil {
			t.Errorf("%q parsed", src)
			continue
		}
		if !strings.Contains(err.Error(), says) {
			t.Errorf("%q was refused with %q, which does not mention %q", src, err, says)
		}
	}
}

func TestACommentIsDropped(t *testing.T) {
	sql, _, err := mustParse(t, "postgres", "select 1{# why #} as k").Render(nil, postgres.Dialect{})
	if err != nil || sql != "select 1 as k" {
		t.Errorf("got %q, %v", sql, err)
	}
}

// A PLACEHOLDER INSIDE A STRING IS A STRING. `'%{{ x }}%'` renders as
// `'%$1%'`, a literal containing a dollar sign, and the value is silently
// never used -- or, worse, the author "fixes" it by building the pattern in
// SQL text. Refused at load, where the author is.
func TestAPlaceholderInsideALiteralOrACommentIsRefused(t *testing.T) {
	for dia, srcs := range map[string][]string{
		"postgres": {
			"select * from t where c like '%{{ x }}%'",
			"select 1 -- {{ x }}",
			"select 1 /* {{ x }} */",
			`select "{{ x }}" from t`,
			"select 1 from t{{ x }}",
		},
		"bigquery": {
			`select * from t where c like "%{{ x }}%"`,
			"select 1 # {{ x }}",
			"select `{{ x }}` from t",
		},
	} {
		for _, src := range srcs {
			if _, err := Parse(dia, src); err == nil {
				t.Errorf("%s: %q parsed", dia, src)
			}
		}
	}
}

// THE PAYLOAD TABLE: whatever a value holds, the SQL text is byte for byte
// the same, and only the arguments differ.
func TestNoValueChangesTheSQLText(t *testing.T) {
	tmpl := mustParse(t, "postgres", "select * from t where w = {{ tenant }} and c = {{ campaign }}")
	payloads := []string{
		"' or 1=1 --", "$1", "@tenant", "}}", "{% endif %}", "{{ tenant }}",
		"a\nb", "a\x00b", strings.Repeat("a", 1<<20), `\'`, "/*", "*/",
	}
	want, _, err := tmpl.Render(Values{"tenant": Bind("acme"), "campaign": Bind("ok")}, postgres.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		sql, args, err := tmpl.Render(Values{"tenant": Bind("acme"), "campaign": Bind(p)}, postgres.Dialect{})
		if err != nil {
			t.Fatalf("%q: %v", p[:min(len(p), 20)], err)
		}
		if sql != want {
			t.Errorf("payload %q changed the SQL to %q", p[:min(len(p), 20)], sql)
		}
		if args[1].Value != p {
			t.Errorf("payload %q did not travel as the argument", p[:min(len(p), 20)])
		}
	}
}

// DETERMINISTIC, because the cache key is a hash of the output: the same
// inputs must give the same bytes, every time, in every order the map
// happens to iterate.
func TestRenderingIsDeterministic(t *testing.T) {
	src := "select {{ a }}, {{ b }}{% if c %}, {{ c }}{% endif %}{% if d or e %}, {{ e }}{% endif %} from t"
	tmpl := mustParse(t, "bigquery", src)
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 10000; i++ {
		vals := Values{"a": Bind(int64(r.Intn(9))), "b": Bind(fmt.Sprint(r.Intn(9)))}
		if r.Intn(2) == 0 {
			vals["c"] = Bind(true)
		}
		if r.Intn(2) == 0 {
			vals["e"] = Bind("e")
		}
		s1, a1, e1 := tmpl.Render(vals, bigquery.Dialect{})
		s2, a2, e2 := tmpl.Render(vals, bigquery.Dialect{})
		if s1 != s2 || !reflect.DeepEqual(a1, a2) || (e1 == nil) != (e2 == nil) {
			t.Fatalf("two renders of %v differ: %q %v / %q %v", vals, s1, a1, s2, a2)
		}
	}
}

// NAMES IS WHAT THE CONFIG CHECKS AGAINST: every name the template uses, in
// the order first used, so "you used `campain`, which is not a parameter"
// can be said at load.
func TestNamesAreEveryNameUsed(t *testing.T) {
	tmpl := mustParse(t, "postgres", "{{ tenant }} {% if b and not c %}{{ a }}{% endif %} {{ tenant }}")
	if got := tmpl.Names(); !reflect.DeepEqual(got, []string{"tenant", "b", "c", "a"}) {
		t.Errorf("names are %v", got)
	}
}

// CONDITIONS SIDE BY SIDE ARE NOT A GLUED WORD: each branch is checked as
// the text it is, and a placeholder that ends one branch does not touch the
// one that starts the next.
func TestPlaceholdersInNeighbouringConditionsParse(t *testing.T) {
	src := "select 1 {% if a %}{{ a }}{% endif %}{% if b %}{{ b }}{% else %}{{ c }}{% endif %}"
	if _, err := Parse("postgres", src); err != nil {
		t.Errorf("refused: %v", err)
	}
	// and a branch inside a literal is still inside it
	if _, err := Parse("postgres", "select '{% if a %}{{ a }}{% endif %}'"); err == nil {
		t.Error("a placeholder inside a literal parsed once it was in a branch")
	}
}

func TestUnconditionalIsAHoleOutsideEveryCondition(t *testing.T) {
	tmpl := mustParse(t, "postgres", "select {{ a }} {% if c %}{{ b }} {{ a }}{% endif %}")
	if !tmpl.Unconditional("a") || tmpl.Unconditional("b") || tmpl.Unconditional("c") {
		t.Error("Unconditional is wrong")
	}
	if !tmpl.IsCondition("c") || tmpl.IsCondition("a") {
		t.Error("IsCondition is wrong")
	}
}

// WHAT THE TOKENIZER CANNOT FOLLOW IS REFUSED, because where it and the
// warehouse disagree a placeholder can hide. Found in review: each of these
// parsed, and in each the warehouse reads `{{ tenant }}` as text.
func TestSQLTheTokenizerCannotFollowIsRefused(t *testing.T) {
	for dia, srcs := range map[string][]string{
		"postgres": {
			"select 1 /* /* */ where t = {{ tenant }} */",
			"select $$ {{ tenant }} $$",
			"select $tag$ {{ tenant }} $tag$",
			`select E'it\' {{ tenant }} '`,
			"select 1 /* a comment */ where t = {{ tenant }}",
		},
		"bigquery": {
			"select ''' ' {{ tenant }} ' '''",
			`select """ " {{ tenant }} " """`,
		},
	} {
		for _, src := range srcs {
			if _, err := Parse(dia, src); err == nil {
				t.Errorf("%s: %q parsed", dia, src)
			}
		}
	}
}

// AND EVERY RENDER IS CHECKED AS IT IS RENDERED. Branches laid end to end at
// parse can balance quotes that one choice of branches leaves open; here
// all-taken reads `' ' $1 ' '`, and a=false renders `' $1 '` -- a
// placeholder inside a string, and a filter that is no filter.
func TestARenderThatPutsAPlaceholderInsideALiteralIsRefused(t *testing.T) {
	tmpl := mustParse(t, "postgres", "select '{% if a %}'{% endif %} {{ x }} {% if a %}'{% endif %}'")
	if _, _, err := tmpl.Render(Values{"a": Bind(true), "x": Bind("v")}, postgres.Dialect{}); err != nil {
		t.Errorf("a=true: %v", err)
	}
	if _, _, err := tmpl.Render(Values{"a": Bind(false), "x": Bind("v")}, postgres.Dialect{}); err == nil {
		t.Error("a=false rendered a placeholder inside a literal")
	}
}
