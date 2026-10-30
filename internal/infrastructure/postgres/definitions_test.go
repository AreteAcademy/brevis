package postgres_test

import (
	"context"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// EVERY PUBLISHED DEFINITION, IN ONE READ, AND ONLY THE CURRENT ONE: a page
// with twenty workflows reads them once, not twenty times, and a workflow the
// prune took out is not in the map.
func TestDefinitionsAreEveryPublishedWorkflow(t *testing.T) {
	pool := insightsDB(t)
	sales := project(t, pool, "sales")
	repo := postgres.NewWorkflowRepo(pool)
	ctx := context.Background()

	publish := func(slug, image, step string) {
		t.Helper()
		w := wf.Workflow{Slug: slug, Name: slug, Kind: wf.KindChain, Image: image,
			Nodes: []wf.Node{{ID: "load", Run: step}}}
		if err := repo.Publicar(ctx, w, sales, "cli"); err != nil {
			t.Fatal(err)
		}
	}
	publish("orders", "python:3.12", "python load.py")
	publish("orders", "python:3.13", "python load.py") // republished: the new one
	publish("refunds", "", "dbt build")
	publish("gone", "", "echo gone")
	if _, err := repo.Podar(ctx, sales, []string{"orders", "refunds"}, "cli"); err != nil {
		t.Fatal(err)
	}

	defs, err := repo.Definitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("definitions = %d, want orders and refunds", len(defs))
	}
	if got := defs["orders"].Image; got != "python:3.13" {
		t.Errorf("orders image = %q, want the republished one", got)
	}
	if got := defs["refunds"].Nodes; len(got) != 1 || got[0].Run != "dbt build" {
		t.Errorf("refunds nodes = %+v", got)
	}
	if _, ok := defs["gone"]; ok {
		t.Error("a pruned workflow is in the map")
	}
}
