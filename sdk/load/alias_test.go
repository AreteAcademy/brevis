package load

import (
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestDeprecatedNamesStillResolve guards the aliases this package kept.
//
// PlanoDeCriacao and ComoCriar shipped in published versions, so they stay
// until v1. Deleting one turns this test into a build failure.
func TestDeprecatedNamesStillResolve(t *testing.T) {
	cfg := &core.LoadConfig{CreateSQL: "CREATE TABLE t (id STRING)"}

	var old ComoCriar
	old, err := PlanoDeCriacao(cfg, "d.t")
	if err != nil {
		t.Fatalf("PlanoDeCriacao: %v", err)
	}
	novo, err := CreationPlan(cfg, "d.t")
	if err != nil {
		t.Fatalf("CreationPlan: %v", err)
	}
	if old != novo {
		t.Fatalf("the alias diverged: PlanoDeCriacao=%v CreationPlan=%v", old, novo)
	}
	if CriarPorSQL != CreateFromSQL || CriarPorSchema != CreateFromSchema {
		t.Fatal("the constant aliases diverged from what they alias")
	}
}
