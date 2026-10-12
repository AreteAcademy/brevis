// Package render turns an endpoint's SQL template into a statement and its
// arguments, and never writes a caller's value into the statement.
//
// A SUBSET OF JINJA AND NOT JINJA. Text, `{{ name }}`, `{# comment #}` and
// `{% if %}/{% elif %}/{% else %}/{% endif %}` over names joined by `not`,
// `and` and `or` -- and a parse error naming whatever else was written. The
// syntax is Jinja's so an editor highlights it; the semantics are smaller on
// purpose. The spike that chose this over gonja (88 packages and 6.8 MB for
// five node kinds) is in the working record, and its other finding is the
// safety argument: a parser that knows only five things refuses the rest by
// not parsing them, where an allowlist over a full engine's AST is a list of
// thirty-seven things to remember to refuse.
//
// `{{ x }}` IS A PLACEHOLDER. It renders as the dialect's `$n` or `@x` and
// the value travels in the arguments, so what a caller sends is data
// whatever it contains. The one exception is a value the caller marks
// Inline -- an enum, because `order by day $1` is not SQL -- and that is
// written into the text only if it is an identifier, checked here again
// rather than trusted.
package render

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/sqltok"
)

// Value is what a name stands for in one render.
type Value struct {
	v      any
	inline bool
}

// Bind is a value that travels as an argument. It is one of the types
// dialect.Arg allows: string, int64, bool or dialect.Date.
func Bind(v any) Value { return Value{v: v} }

// Bound is what the value binds as, or nil for none or an inline one.
func (v Value) Bound() any {
	if v.inline {
		return nil
	}
	return v.v
}

// Inline is a value written into the SQL text. Only an identifier is
// accepted, at render time, whatever the caller validated before.
func Inline(identifier string) Value { return Value{v: identifier, inline: true} }

// Values is one request's names and what they stand for. A name that is
// absent is false in a condition and an error in `{{ }}`.
type Values map[string]Value

// Template is a parsed endpoint SQL.
type Template struct {
	dialect string
	nodes   []node
	names   []string

	// top is every name written as `{{ }}` outside every condition, and
	// conds every name a condition tests.
	top, conds map[string]bool
}

// Unconditional reports whether `{{ name }}` is written outside every
// condition -- in every statement this template can render, whatever the
// caller sends. What the API asks of `tenant`: a filter that only exists in
// some branch is a filter the caller can choose to leave out.
func (t *Template) Unconditional(name string) bool { return t.top[name] }

// AllText is every branch's text laid end to end, each `{{ }}` as NULL: what
// a caller reads to know which relations the template can read -- in any
// branch, because a model read only when a parameter is sent is still one
// whose rebuild changes the answer.
func (t *Template) AllText() string {
	var b strings.Builder
	var walk func([]node)
	walk = func(ns []node) {
		for _, n := range ns {
			switch n := n.(type) {
			case text:
				b.WriteString(string(n))
			case hole:
				b.WriteString(" NULL ")
			case *cond:
				for _, br := range n.branches {
					b.WriteByte(' ')
					walk(br.body)
					b.WriteByte(' ')
				}
				b.WriteByte(' ')
				walk(n.orElse)
				b.WriteByte(' ')
			}
		}
	}
	walk(t.nodes)
	return b.String()
}

// IsCondition reports whether a condition tests the name.
func (t *Template) IsCondition(name string) bool { return t.conds[name] }

// Names is every name the template uses, in the order it is first used --
// what the config checks a template against.
func (t *Template) Names() []string { return append([]string(nil), t.names...) }

type node interface{}

type text string

type hole string

type cond struct {
	branches []branch // the `if` and each `elif`
	orElse   []node
	hasElse  bool
}

type branch struct {
	test expr
	body []node
}

type expr interface{ eval(Values) bool }

type nameExpr string
type notExpr struct{ x expr }
type andExpr struct{ l, r expr }
type orExpr struct{ l, r expr }

func (n nameExpr) eval(v Values) bool {
	val, there := v[string(n)]
	if !there {
		return false
	}
	if b, is := val.v.(bool); is {
		return b
	}
	return true
}
func (n notExpr) eval(v Values) bool { return !n.x.eval(v) }
func (a andExpr) eval(v Values) bool { return a.l.eval(v) && a.r.eval(v) }
func (o orExpr) eval(v Values) bool  { return o.l.eval(v) || o.r.eval(v) }

// identifier is what a name -- and an inlined value -- may be. ASCII only:
// an identifier a warehouse folds or normalises differently from how it was
// checked is the gap an inlined value must not have.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsIdentifier reports whether s may be written into the SQL as an Inline
// value -- the rule Render applies, for a caller to apply at load.
func IsIdentifier(s string) bool { return identifier.MatchString(s) }

// Parse reads a template written for one dialect. The dialect is needed
// because whether `{{ x }}` sits inside a string or a comment is a question
// about that dialect's SQL: `#` is a comment in BigQuery and an operator in
// Postgres.
func Parse(dialectName, src string) (*Template, error) {
	p := &parser{src: src, seen: map[string]bool{}, conds: map[string]bool{}}
	nodes, end, err := p.block(true)
	if err != nil {
		return nil, err
	}
	if end != "" {
		return nil, fmt.Errorf("`{%% %s %%}` with no `{%% if %%}` open", end)
	}
	t := &Template{dialect: dialectName, nodes: nodes, names: p.names, top: map[string]bool{}, conds: p.conds}
	for _, n := range nodes {
		if h, is := n.(hole); is {
			t.top[string(h)] = true
		}
	}
	if err := checkPlacement(dialectName, t); err != nil {
		return nil, err
	}
	return t, nil
}

type parser struct {
	src   string
	pos   int
	names []string
	seen  map[string]bool
	conds map[string]bool
}

func (p *parser) use(name string) {
	if !p.seen[name] {
		p.seen[name] = true
		p.names = append(p.names, name)
	}
}

// block reads nodes until the end of the source (top level) or until an
// `elif`, `else` or `endif`, which it returns unconsumed in meaning: the
// caller decides whether that tag is allowed where it was found.
func (p *parser) block(top bool) (nodes []node, stop string, err error) {
	var stopTag string
	for p.pos < len(p.src) {
		rest := p.src[p.pos:]
		i := indexOfTag(rest)
		if i < 0 {
			nodes = append(nodes, text(rest))
			p.pos = len(p.src)
			break
		}
		if i > 0 {
			nodes = append(nodes, text(rest[:i]))
		}
		p.pos += i
		open := p.src[p.pos : p.pos+2]
		closeWith := map[string]string{"{{": "}}", "{%": "%}", "{#": "#}"}[open]
		j := strings.Index(p.src[p.pos+2:], closeWith)
		if j < 0 {
			return nil, "", fmt.Errorf("`%s` at offset %d is never closed with `%s`", open, p.pos, closeWith)
		}
		inner := p.src[p.pos+2 : p.pos+2+j]
		p.pos += 2 + j + 2

		switch open {
		case "{#":
			continue
		case "{{":
			name, err := holeName(inner)
			if err != nil {
				return nil, "", err
			}
			p.use(name)
			nodes = append(nodes, hole(name))
		case "{%":
			if strings.HasPrefix(inner, "-") || strings.HasSuffix(inner, "-") {
				return nil, "", fmt.Errorf("`{%%%s%%}`: whitespace control (`-`) is not supported", inner)
			}
			words := strings.Fields(inner)
			if len(words) == 0 {
				return nil, "", fmt.Errorf("`{%% %%}` is empty")
			}
			switch words[0] {
			case "if":
				c, err := p.cond(words[1:], inner)
				if err != nil {
					return nil, "", err
				}
				nodes = append(nodes, c)
			case "elif", "else", "endif":
				stopTag = inner
				if top {
					return nil, words[0], nil
				}
				return nodes, stopTag, nil
			default:
				return nil, "", fmt.Errorf("`{%% %s %%}`: `%s` is not supported; "+
					"only if, elif, else and endif are", strings.TrimSpace(inner), words[0])
			}
		}
	}
	if !top {
		return nil, "", fmt.Errorf("an `{%% if %%}` is never closed with `{%% endif %%}`")
	}
	return nodes, "", nil
}

func (p *parser) cond(test []string, inner string) (*cond, error) {
	c := &cond{}
	for {
		e, err := p.expr(test, inner)
		if err != nil {
			return nil, err
		}
		body, stop, err := p.block(false)
		if err != nil {
			return nil, err
		}
		c.branches = append(c.branches, branch{test: e, body: body})
		words := strings.Fields(stop)
		switch words[0] {
		case "endif":
			return c, nil
		case "elif":
			test, inner = words[1:], stop
			continue
		case "else":
			if len(words) > 1 {
				return nil, fmt.Errorf("`{%% %s %%}`: else takes no condition", strings.TrimSpace(stop))
			}
			body, stop, err := p.block(false)
			if err != nil {
				return nil, err
			}
			c.orElse, c.hasElse = body, true
			switch strings.Fields(stop)[0] {
			case "endif":
				return c, nil
			case "elif":
				return nil, fmt.Errorf("an `{%% elif %%}` after `{%% else %%}`")
			default:
				return nil, fmt.Errorf("a second `{%% else %%}` in one `{%% if %%}`")
			}
		}
	}
}

// expr parses `or` over `and` over `not` over names: the precedence Jinja
// and Python give them, and no parentheses -- a condition that needs them
// is a condition to split into two parameters.
func (p *parser) expr(words []string, inner string) (expr, error) {
	if len(words) == 0 {
		return nil, fmt.Errorf("`{%% %s %%}` has no condition", strings.TrimSpace(inner))
	}
	pos := 0
	var or, and, unary func() (expr, error)
	unary = func() (expr, error) {
		if pos >= len(words) {
			return nil, fmt.Errorf("the condition `%s` ends where a name was expected", strings.Join(words, " "))
		}
		w := words[pos]
		pos++
		if w == "not" {
			x, err := unary()
			return notExpr{x}, err
		}
		if !identifier.MatchString(w) || w == "and" || w == "or" {
			return nil, fmt.Errorf("`%s` in the condition `%s`: a condition is names "+
				"joined by not, and, or", w, strings.Join(words, " "))
		}
		p.use(w)
		p.conds[w] = true
		return nameExpr(w), nil
	}
	and = func() (expr, error) {
		l, err := unary()
		for err == nil && pos < len(words) && words[pos] == "and" {
			pos++
			var r expr
			r, err = unary()
			l = andExpr{l, r}
		}
		return l, err
	}
	or = func() (expr, error) {
		l, err := and()
		for err == nil && pos < len(words) && words[pos] == "or" {
			pos++
			var r expr
			r, err = and()
			l = orExpr{l, r}
		}
		return l, err
	}
	e, err := or()
	if err != nil {
		return nil, err
	}
	if pos < len(words) {
		return nil, fmt.Errorf("`%s` in the condition `%s`: a condition is names "+
			"joined by not, and, or", words[pos], strings.Join(words, " "))
	}
	return e, nil
}

func holeName(inner string) (string, error) {
	if strings.HasPrefix(inner, "-") || strings.HasSuffix(inner, "-") {
		return "", fmt.Errorf("`{{%s}}`: whitespace control (`-`) is not supported", inner)
	}
	name := strings.TrimSpace(inner)
	if name == "" {
		return "", fmt.Errorf("`{{ }}` is empty: write a parameter's name between the braces")
	}
	if !identifier.MatchString(name) || name == "and" || name == "or" || name == "not" {
		return "", fmt.Errorf("`{{ %s }}`: only a parameter's name can go between {{ and }} "+
			"-- no filters, attributes, indexes or literals", name)
	}
	return name, nil
}

func indexOfTag(s string) int {
	best := -1
	for _, open := range []string{"{{", "{%", "{#"} {
		if i := strings.Index(s, open); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// checkPlacement refuses a placeholder the SQL would not read as one.
//
// `'%{{ x }}%'` renders as `'%$1%'`: a string containing a dollar sign, the
// value never used, and nothing failing. The same inside a comment, a quoted
// identifier, or glued to a word (`t{{ x }}` is the identifier `t$1`). So
// every `{{ }}` is replaced by a word nobody writes, every branch's text is
// laid end to end, and the dialect's own tokenizer -- the one `refs` and
// `serve` trust -- must find each of those words standing alone.
func checkPlacement(dialectName string, t *Template) error {
	var b strings.Builder
	var holes []string
	var walk func([]node)
	walk = func(ns []node) {
		for _, n := range ns {
			switch n := n.(type) {
			case text:
				b.WriteString(string(n))
			case hole:
				b.WriteString(sentinel(len(holes)))
				holes = append(holes, string(n))
			case *cond:
				// A SPACE AT EVERY BRANCH'S EDGES, or two conditions side by
				// side lay their placeholders end to end and read as one
				// glued word. It cannot hide a placeholder that is inside a
				// string: a space inside a literal is still inside it.
				for _, br := range n.branches {
					b.WriteByte(' ')
					walk(br.body)
					b.WriteByte(' ')
				}
				b.WriteByte(' ')
				walk(n.orElse)
				b.WriteByte(' ')
			}
		}
	}
	walk(t.nodes)
	if err := followable(dialectName, b.String()); err != nil {
		return err
	}
	return placed(dialectName, b.String(), holes)
}

// The constructs where the tokenizer and the warehouse read a statement
// differently. Found in review: in each, sqltok sees `{{ x }}` as code and
// the warehouse sees it as text -- a nested Postgres comment, a dollar-quoted
// or E” string, a BigQuery triple-quoted one. Refused rather than taught,
// because teaching the tokenizer one of them leaves the next one, and an
// endpoint's SQL has `--` and `{# #}` for comments and ” for strings.
var (
	dollarQuote = regexp.MustCompile(`\$[A-Za-z_0-9]*\$`)
	escapeQuote = regexp.MustCompile(`(^|[^A-Za-z0-9_])[eE]'`)
)

func followable(dialectName, sql string) error {
	if strings.Contains(sql, "/*") {
		return fmt.Errorf("a /* block comment */ is not allowed in an endpoint's SQL: " +
			"write -- or {# #}, which no reader can disagree about")
	}
	switch dialectName {
	case "postgres":
		if dollarQuote.MatchString(sql) {
			return fmt.Errorf("a dollar-quoted string ($$ or $tag$) is not allowed in an endpoint's SQL: write ''")
		}
		if escapeQuote.MatchString(sql) {
			return fmt.Errorf("an E'' string is not allowed in an endpoint's SQL: write ''")
		}
	case "bigquery":
		if strings.Contains(sql, "'''") || strings.Contains(sql, `"""`) {
			return fmt.Errorf("a triple-quoted string is not allowed in an endpoint's SQL: write '' or \"\"")
		}
	}
	return nil
}

// placed reports whether every sentinel stands alone as a word in sql.
func placed(dialectName, sql string, holes []string) error {
	if len(holes) == 0 {
		return nil
	}
	toks, whole := sqltok.Tokenize(dialectName, sql)
	if !whole {
		return fmt.Errorf("the SQL has a quote or a comment that never closes, so where " +
			"its placeholders sit cannot be told")
	}
	found := map[string]bool{}
	for _, tok := range toks {
		if tok.Kind != sqltok.Word {
			continue
		}
		// BigQuery lets a word carry hyphens (`my-project`), so `{{ n }}-1`
		// is one token there; the parts are what stand alone.
		for _, part := range strings.Split(tok.Lower, "-") {
			found[part] = true
		}
	}
	for i, name := range holes {
		if !found[sentinel(i)] {
			return fmt.Errorf("`{{ %s }}` sits inside a string, a comment or a quoted name, "+
				"or touches a word: a placeholder there is text and its value is never "+
				"used. Build a pattern in SQL instead -- `'%%' || {{ %s }} || '%%'`", name, name)
		}
	}
	return nil
}

func sentinel(i int) string { return "zzbrevisrenderhole" + strconv.Itoa(i) + "zz" }

// Render writes the statement for one request.
//
// DETERMINISTIC: the same template, values and dialect give the same bytes
// and the same arguments, because the cache key is a hash of them. Arguments
// are numbered in the order their placeholders are written, and a name
// written twice is bound once.
func (t *Template) Render(vals Values, b dialect.Binder) (string, []dialect.Arg, error) {
	// THE PROBE IS RENDERED BESIDE THE STATEMENT, the same branches with a
	// sentinel where each placeholder goes, and checked as parse checked
	// the whole template: branches laid end to end can balance quotes that
	// one particular choice of branches leaves open. Found in review.
	var out, probe strings.Builder
	var holes []string
	var args []dialect.Arg
	bound := map[string]string{}
	var walk func([]node) error
	walk = func(ns []node) error {
		for _, n := range ns {
			switch n := n.(type) {
			case text:
				out.WriteString(string(n))
				probe.WriteString(string(n))
			case hole:
				name := string(n)
				v, there := vals[name]
				if !there {
					return fmt.Errorf("`{{ %s }}` has no value: wrap it in "+
						"`{%% if %s %%}`, or give the parameter a default", name, name)
				}
				if v.inline {
					s, _ := v.v.(string)
					if !identifier.MatchString(s) {
						return fmt.Errorf("`%s` is written into the SQL and must be an "+
							"identifier, and %q is not", name, s)
					}
					out.WriteString(s)
					probe.WriteString(s)
					continue
				}
				probe.WriteString(sentinel(len(holes)))
				holes = append(holes, name)
				ph, done := bound[name]
				if !done {
					args = append(args, dialect.Arg{Name: name, Value: v.v})
					ph = b.Placeholder(len(args), name)
					bound[name] = ph
				}
				out.WriteString(ph)
			case *cond:
				taken := false
				for _, br := range n.branches {
					if br.test.eval(vals) {
						if err := walk(br.body); err != nil {
							return err
						}
						taken = true
						break
					}
				}
				if !taken && n.hasElse {
					if err := walk(n.orElse); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := walk(t.nodes); err != nil {
		return "", nil, err
	}
	if err := placed(t.dialect, probe.String(), holes); err != nil {
		return "", nil, err
	}
	return out.String(), args, nil
}
