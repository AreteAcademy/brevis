package execution_test

import (
	"context"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

func task(nome string) execution.Task {
	return execution.FuncTask{Nome: nome, Fn: func(context.Context, execution.Input) error { return nil }}
}

// Overwriting a registration in silence is a bug that only shows in production,
// when the wrong task runs.
func TestRegisterRefusesADuplicate(t *testing.T) {
	r := execution.NewRegistry()
	if err := r.Register(task("sync")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(task("sync")); err == nil {
		t.Fatal("esperava recusa de nome duplicado")
	}
}

func TestRegisterRefusesAnEmptyName(t *testing.T) {
	if err := execution.NewRegistry().Register(task("")); err == nil {
		t.Fatal("esperava recusa de nome vazio")
	}
}

func TestTheNamesComeSorted(t *testing.T) {
	r := execution.NewRegistry()
	for _, n := range []string{"zeta", "alfa", "meio"} {
		r.MustRegister(task(n))
	}
	got := r.Nomes()
	if len(got) != 3 || got[0] != "alfa" || got[2] != "zeta" {
		t.Errorf("Nomes() = %v, wanted sorted", got)
	}
}

func TestInputTextoValidaParametro(t *testing.T) {
	in := execution.Input{With: map[string]any{"image": "acme:1.0", "porta": 8080}}

	if v, err := in.Texto("image"); err != nil || v != "acme:1.0" {
		t.Errorf("Texto(image) = %q, %v", v, err)
	}
	if _, err := in.Texto("ausente"); err == nil {
		t.Error("expected a missing-parameter error")
	}
	if _, err := in.Texto("porta"); err == nil {
		t.Error("expected a type error: porta is an int, not text")
	}
}
