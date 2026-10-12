package api

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/connections"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// fixture writes an endpoint file and a registry beside it, and loads both.
func fixture(t *testing.T, file string, extra map[string]string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APP_DSN", "postgres://reader:pw@db:5432/app?sslmode=disable")
	reg := filepath.Join(dir, "brevis.yaml")
	if err := os.WriteFile(reg, []byte(`
connections:
  - name: warehouse
    dialect: postgres
    dsn_from: APP_DSN
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range extra {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(p, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := connections.Load(reg)
	if err != nil {
		t.Fatal(err)
	}
	return Load(p, r)
}

const good = `
name: metrics-api
connection: warehouse
listen:
  addr: :8080
endpoints:
  - name: clicks
    path: /v1/clicks
    params:
      campaign: {type: string, max_len: 64}
    sql_file: endpoints/clicks.sql
  - name: orders
    path: /v1/orders
    tenant: none
    sql: select 1 as k
`

func TestAGoodFileLoads(t *testing.T) {
	cfg, err := fixture(t, good, map[string]string{
		"endpoints/clicks.sql": "select day from marts.clicks where w = {{ tenant }} and c = {{ campaign }}",
	})
	if err != nil {
		t.Fatalf("a good file was refused: %v", err)
	}
	if cfg.Name != "metrics-api" || len(cfg.Endpoints) != 2 {
		t.Fatalf("loaded %+v", cfg)
	}
	// sql_file IS RELATIVE TO THE FILE, not to wherever the binary was
	// started: the two travel together in a ConfigMap or a repository.
	if got := cfg.Endpoints[0].Template.Names(); len(got) != 2 || got[1] != "campaign" {
		t.Errorf("clicks.sql was read as using %v", got)
	}
	if cfg.Dialect.Name() != "postgres" {
		t.Errorf("the dialect is %s", cfg.Dialect.Name())
	}
}

// EVERY REFUSAL NAMES THE SETTING, because the person reading it is the one
// who has to go and change it.
func TestWhatCannotBeHonouredIsRefusedByName(t *testing.T) {
	cases := map[string]struct{ file, says string }{
		"unknown connection":             {strings.Replace(good, "connection: warehouse", "connection: nope", 1), "`connection` is \"nope\""},
		"no connection":                  {strings.Replace(good, "connection: warehouse\n", "", 1), "`connection`"},
		"both sql and file":              {strings.Replace(good, "sql: select 1 as k", "sql: select 1\n    sql_file: endpoints/clicks.sql", 1), "both `sql` and `sql_file`"},
		"neither":                        {strings.Replace(good, "    sql: select 1 as k\n", "", 1), "neither `sql` nor `sql_file`"},
		"a path twice":                   {strings.Replace(good, "path: /v1/orders", "path: /v1/clicks", 1), "/v1/clicks"},
		"a name twice":                   {strings.Replace(good, "name: orders", "name: clicks", 1), "\"clicks\""},
		"an unversioned path":            {strings.Replace(good, "path: /v1/orders", "path: /orders", 1), "/orders"},
		"an unknown key":                 {strings.Replace(good, "listen:", "lisen:\n  addr: x\nlisten:", 1), "lisen"},
		"an unknown type":                {strings.Replace(good, "type: string", "type: float", 1), "float"},
		"a string with no max":           {strings.Replace(good, ", max_len: 64", "", 1), "max_len"},
		"an undeclared name":             {strings.Replace(good, "sql: select 1 as k", "sql: select {{ region }}", 1), "region"},
		"a template that does not parse": {strings.Replace(good, "sql: select 1 as k", "sql: \"select {{ x | safe }}\"", 1), "x | safe"},
		"no endpoints":                   {"name: x\nconnection: warehouse\nlisten: {addr: ':1'}\n", "no `endpoints`"},
	}
	for name, c := range cases {
		_, err := fixture(t, c.file, map[string]string{
			"endpoints/clicks.sql": "select day from marts.clicks where w = {{ tenant }} and c = {{ campaign }}",
		})
		if err == nil {
			t.Errorf("%s: loaded", name)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: refused with %q, which does not say %q", name, err, c.says)
		}
	}
}

// A CREDENTIAL IS NEVER A SETTING. Every field this file can hold that is
// named like a key, a DSN, a password, a token or a secret must end in
// `_from` and hold a variable's name -- the gateway's rule, enforced the
// gateway's way: by walking the struct, so a field added next year is
// checked the day it is added.
func TestNoSettingIsNamedLikeACredentialWithoutFrom(t *testing.T) {
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if tag == "" || tag == "-" {
				continue
			}
			lower := strings.ToLower(tag)
			for _, word := range []string{"key", "dsn", "password", "token", "secret"} {
				if strings.Contains(lower, word) && !strings.HasSuffix(lower, "_from") {
					t.Errorf("%s%s holds something named like a credential and does not end in _from", path, tag)
				}
			}
			walk(f.Type, path+tag+".")
		}
	}
	walk(reflect.TypeOf(Config{}), "")
}

// THE TENANT FILTER IS UNCONDITIONAL. Found in review: a template that used
// `{{ tenant }}` only inside `{% if campaign %}` loaded, and a caller who
// left `campaign` out read every tenant's rows -- the caller decided whether
// the filter existed. So `{{ tenant }}` must appear outside every condition,
// and `tenant` may not be a condition at all.
func TestTheTenantFilterCannotBeConditional(t *testing.T) {
	for name, sql := range map[string]string{
		"only inside an if":   "select * from t {% if campaign %}where w = {{ tenant }} and c = {{ campaign }}{% endif %}",
		"only inside an else": "select * from t {% if campaign %}where c = {{ campaign }}{% else %}where w = {{ tenant }}{% endif %}",
		"as a condition":      "select * from t where w = {{ tenant }} {% if tenant %}{% endif %}",
		"only a condition":    "select * from t {% if tenant %}{% endif %}{% if campaign %}{{ campaign }}{% endif %}",
	} {
		e := &Endpoint{Name: "e", Path: "/v1/e", SQL: sql,
			Params: map[string]Param{"campaign": {Type: "string", MaxLen: 8}}}
		if err := e.parse(postgresDialect); err == nil || !strings.Contains(err.Error(), "tenant") {
			t.Errorf("%s: %v", name, err)
		}
	}
	e := &Endpoint{Name: "e", Path: "/v1/e", Params: map[string]Param{"campaign": {Type: "string", MaxLen: 8}},
		SQL: "select * from t where w = {{ tenant }} {% if campaign %}and c = {{ campaign }} and w = {{ tenant }}{% endif %}"}
	if err := e.parse(postgresDialect); err != nil {
		t.Errorf("an unconditional filter plus a conditional use was refused: %v", err)
	}
}

var postgresDialect = postgres.Dialect{}
