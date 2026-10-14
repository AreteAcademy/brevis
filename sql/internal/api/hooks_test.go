package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/connections"
)

func keepRequest(context.Context, *Request) error          { return nil }
func rowsAsData(_ context.Context, r *Result) (any, error) { return r.Rows, nil }

// withHooks loads an endpoint file the way fixture does, with a registry of
// hooks the test chooses.
func withHooks(t *testing.T, file string, hooks *Hooks) (*Config, error) {
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
	p := filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(p, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := connections.Load(reg)
	if err != nil {
		t.Fatal(err)
	}
	return Load(p, r, hooks)
}

func hooked(hook string) string {
	return `
name: metrics-api
connection: warehouse
endpoints:
  - name: orders
    path: /v1/orders
    tenant: none
    sql: select 1 as k
    hook: ` + hook + `
`
}

func TestAHookRegisteredByNameLoads(t *testing.T) {
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", keepRequest)
	hooks.MustRegisterAfter("to_series", rowsAsData)
	cfg, err := withHooks(t, hooked(`{before: check_window, after: to_series}`), hooks)
	if err != nil {
		t.Fatalf("a file naming registered hooks was refused: %v", err)
	}
	e := cfg.Endpoints[0]
	if e.before == nil || e.after == nil {
		t.Fatalf("the hooks were not resolved: before=%v after=%v", e.before != nil, e.after != nil)
	}
}

func TestEitherHookIsOptional(t *testing.T) {
	hooks := NewHooks()
	hooks.MustRegisterAfter("to_series", rowsAsData)
	cfg, err := withHooks(t, hooked(`{after: to_series}`), hooks)
	if err != nil {
		t.Fatalf("an after alone was refused: %v", err)
	}
	if cfg.Endpoints[0].before != nil {
		t.Error("an endpoint with no before got one")
	}
}

// RESOLVED AT LOAD, NOT PER REQUEST: a file naming a hook nobody registered
// stops the API from starting, instead of answering 500 to every caller of
// that endpoint for a reason only the log has.
func TestAnUnregisteredHookIsRefusedAtLoadNamingWhatExists(t *testing.T) {
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", keepRequest)
	hooks.MustRegisterBefore("check_tenant", keepRequest)
	_, err := withHooks(t, hooked(`{before: chek_window}`), hooks)
	if err == nil {
		t.Fatal("a hook nobody registered was accepted")
	}
	for _, want := range []string{"orders", "chek_window", "check_tenant, check_window"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// THE PUBLISHED BINARY HAS NO HOOKS, and its refusal is the same path as a
// misspelt name -- with the one thing a reader of it needs: where a binary
// with hooks comes from.
func TestNoHooksRegisteredPointsAtTheExample(t *testing.T) {
	for name, hooks := range map[string]*Hooks{"empty": NewHooks(), "nil": nil} {
		t.Run(name, func(t *testing.T) {
			_, err := withHooks(t, hooked(`{after: to_series}`), hooks)
			if err == nil || !strings.Contains(err.Error(), "sql/example") {
				t.Fatalf("want a refusal pointing at sql/example, got %v", err)
			}
		})
	}
}

// A BEFORE IS NOT AN AFTER. They take different arguments; a name found in
// the wrong registry is a hook that cannot be called.
func TestABeforeIsNotFoundAsAnAfter(t *testing.T) {
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", keepRequest)
	if _, err := withHooks(t, hooked(`{after: check_window}`), hooks); err == nil {
		t.Fatal("a before was accepted as an after")
	}
}

func TestAnEmptyHookNameIsRefused(t *testing.T) {
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", keepRequest)
	_, err := withHooks(t, hooked(`{before: ""}`), hooks)
	if err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("an empty before was not refused with what to do: %v", err)
	}
}

func TestAnUnknownHookKeyIsRefused(t *testing.T) {
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", keepRequest)
	if _, err := withHooks(t, hooked(`{befor: check_window}`), hooks); err == nil {
		t.Fatal("`befor:` was accepted")
	}
}

// A DUPLICATE IS REFUSED, NOT OVERWRITTEN: a silently replaced registration
// is the wrong hook running in production.
func TestRegisteringANameTwiceIsRefused(t *testing.T) {
	hooks := NewHooks()
	if err := hooks.RegisterBefore("check_window", keepRequest); err != nil {
		t.Fatal(err)
	}
	if err := hooks.RegisterBefore("check_window", keepRequest); err == nil {
		t.Error("a second before under the same name was accepted")
	}
	if err := hooks.RegisterAfter("to_series", rowsAsData); err != nil {
		t.Fatal(err)
	}
	if err := hooks.RegisterAfter("to_series", rowsAsData); err == nil {
		t.Error("a second after under the same name was accepted")
	}
}

func TestARegistrationNeedsANameAndAFunction(t *testing.T) {
	hooks := NewHooks()
	if err := hooks.RegisterBefore(" ", keepRequest); err == nil {
		t.Error("a blank name was accepted")
	}
	if err := hooks.RegisterAfter("to_series", nil); err == nil {
		t.Error("a nil after was accepted")
	}
}

func TestMustRegisterPanicsOnARefusal(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustRegisterBefore did not panic on a duplicate")
		}
	}()
	hooks := NewHooks()
	hooks.MustRegisterBefore("check_window", keepRequest)
	hooks.MustRegisterBefore("check_window", keepRequest)
}
