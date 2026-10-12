package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

func typed(t *testing.T, params map[string]Param, sql string) *Endpoint {
	t.Helper()
	e := &Endpoint{Name: "e", Path: "/v1/e", Params: params, SQL: sql, Tenant: "none"}
	if err := e.check(); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return e
}

func valuesFor(e *Endpoint, query string) (render.Values, error) {
	return e.values(httptest.NewRequest("GET", "/v1/e?"+query, nil))
}

func ptr[T any](v T) *T { return &v }

// EACH TYPE BECOMES THE GO VALUE ITS DIALECT BINDS, and nothing looser: an
// int is an int64, a date a dialect.Date, a bool a bool, and an enum is the
// one value that is inlined -- after it matched a declared value exactly.
func TestEachTypeBecomesTheValueItBindsAs(t *testing.T) {
	e := typed(t, map[string]Param{
		"n":     {Type: "int", Min: ptr(int64(1)), Max: ptr(int64(100))},
		"d":     {Type: "date"},
		"b":     {Type: "bool"},
		"order": {Type: "enum", Values: []string{"asc", "desc"}},
	}, "select {{ n }}, {{ d }}, {{ b }} order by 1 {{ order }}")

	vals, err := valuesFor(e, "n=42&d=2026-10-05&b=true&order=desc")
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := e.Template.Render(vals, postgres.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	if sql != "select $1, $2, $3 order by 1 desc" {
		t.Errorf("sql %q", sql)
	}
	want := []any{int64(42), dialect.Date{Year: 2026, Month: time.October, Day: 5}, true}
	for i, w := range want {
		if args[i].Value != w {
			t.Errorf("arg %d is %#v, wanted %#v", i, args[i].Value, w)
		}
	}
}

func TestAValueItsTypeCannotHoldIsA400NamingTheParameter(t *testing.T) {
	e := typed(t, map[string]Param{
		"n":     {Type: "int", Min: ptr(int64(1)), Max: ptr(int64(100))},
		"d":     {Type: "date"},
		"b":     {Type: "bool"},
		"order": {Type: "enum", Values: []string{"asc", "desc"}},
	}, "select 1 {% if n %}{{ n }}{% endif %}{% if d %}{{ d }}{% endif %}{% if b %}{{ b }}{% endif %}{% if order %}{{ order }}{% endif %}")
	for query, says := range map[string]string{
		"n=4.2":                  "n",
		"n=0x10":                 "n",
		"n=0":                    "at least 1",
		"n=101":                  "at most 100",
		"n=99999999999999999999": "n",
		"d=2026-13-01":           "d",
		"d=2026-02-30":           "d",
		"d=05/10/2026":           "YYYY-MM-DD",
		"d=2026-10-05T00:00:00Z": "YYYY-MM-DD",
		"b=yes":                  "true or false",
		"b=1":                    "true or false",
		"order=DESC":             "asc, desc",
		"order=desc;":            "does not parse",
		"order=desc%3B":          "asc, desc",
	} {
		if _, err := valuesFor(e, query); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: %v (wanted it to mention %q)", query, err, says)
		}
	}
}

// A DEFAULT IS A VALUE THE CALLER DID NOT SEND, and it goes through the
// same parsing as one they did -- so a default its own type cannot hold is
// refused at load, not discovered by the first request that omits it.
func TestADefaultStandsInForAnOmittedValue(t *testing.T) {
	e := typed(t, map[string]Param{
		"order": {Type: "enum", Values: []string{"asc", "desc"}, Default: ptr("desc")},
		"n":     {Type: "int", Default: ptr("10")},
	}, "select {{ n }} order by 1 {{ order }}")
	vals, err := valuesFor(e, "")
	if err != nil {
		t.Fatal(err)
	}
	sql, args, err := e.Template.Render(vals, postgres.Dialect{})
	if err != nil || sql != "select $1 order by 1 desc" || args[0].Value != int64(10) {
		t.Errorf("%q %v %v", sql, args, err)
	}
}

func TestARequiredParameterThatIsAbsentIsA400(t *testing.T) {
	e := typed(t, map[string]Param{"d": {Type: "date", Required: true}}, "select {{ d }}")
	if _, err := valuesFor(e, ""); err == nil || !strings.Contains(err.Error(), "d is required") {
		t.Errorf("got %v", err)
	}
}

func TestADeclarationThatCannotBeHonouredIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		p    Param
		says string
	}{
		"a default its type cannot hold":          {Param{Type: "int", Default: ptr("ten")}, "default"},
		"required with a default":                 {Param{Type: "int", Required: true, Default: ptr("1")}, "required"},
		"an enum with no values":                  {Param{Type: "enum"}, "values"},
		"an enum value that is not an identifier": {Param{Type: "enum", Values: []string{"desc; drop"}}, "desc; drop"},
		"an enum value twice":                     {Param{Type: "enum", Values: []string{"a", "a"}}, "twice"},
		"min above max":                           {Param{Type: "int", Min: ptr(int64(5)), Max: ptr(int64(1))}, "min"},
		"max_len on an int":                       {Param{Type: "int", MaxLen: 3}, "max_len"},
		"values on a string":                      {Param{Type: "string", MaxLen: 3, Values: []string{"a"}}, "values"},
		"min on a date":                           {Param{Type: "date", Min: ptr(int64(1))}, "min"},
	} {
		e := &Endpoint{Name: "e", Path: "/v1/e", Params: map[string]Param{"x": c.p}, SQL: "select 1", Tenant: "none"}
		err := e.check()
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v (wanted it to mention %q)", name, err, c.says)
		}
	}
}

// A REF IS A DEFINITION WRITTEN ONCE, in the file's `params:`, and used by
// name. Copied, not shared: an endpoint that narrows nothing still gets its
// own value.
func TestARefTakesTheFilesDefinition(t *testing.T) {
	cfg, err := fixture(t, `
name: x
connection: warehouse
listen: {addr: ':1'}
params:
  order: {type: enum, values: [asc, desc], default: desc}
endpoints:
  - name: e
    path: /v1/e
    tenant: none
    params:
      order: {ref: order}
    sql: select 1 order by 1 {{ order }}
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Endpoints[0].Params["order"]; p.Type != "enum" || p.Default == nil || *p.Default != "desc" {
		t.Errorf("the ref resolved to %+v", p)
	}
	for name, c := range map[string]struct{ ref, says string }{
		"an undefined ref":       {"{ref: sort}", "sort"},
		"a ref with more fields": {"{ref: order, default: asc}", "nothing else"},
	} {
		_, err := fixture(t, strings.Replace(`
name: x
connection: warehouse
listen: {addr: ':1'}
params:
  order: {type: enum, values: [asc, desc], default: desc}
endpoints:
  - name: e
    path: /v1/e
    tenant: none
    params:
      order: REF
    sql: select 1 order by 1 {{ order }}
`, "REF", c.ref, 1), nil)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
