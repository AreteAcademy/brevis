package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func groupedWorkflow(slug, group string, limit int) wf.Workflow {
	return wf.Workflow{Slug: slug, Name: slug, Kind: wf.KindChain, MaxActive: limit, Group: group,
		Nodes: []wf.Node{{ID: "a", Run: "echo ok"}}}
}

func project(t *testing.T, pool *postgres.Pool, slug string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO projects (id, slug, name) VALUES ($1, $2, $2)`, id, slug); err != nil {
		t.Fatal(err)
	}
	return id
}

// `publish one-file.yaml` sees one file. The other members of its group are in
// the database, and the limit rule has to hold against them too, or publishing
// the files one at a time is a way around it.
func TestAPartialPublishMeetsTheGroupsPublishedMembers(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)
	p := project(t, pool, "p")
	if err := repo.Publicar(ctx, groupedWorkflow("daily", "jaffle_models", 1), p); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publicar(ctx, groupedWorkflow("loose", "", 3), p); err != nil {
		t.Fatal(err)
	}

	others, err := repo.PublishedGroupMembers(ctx, p, []string{"every_15"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 1 || others[0].Slug != "daily" || others[0].Group != "jaffle_models" || others[0].MaxActive != 1 {
		t.Fatalf("published members = %+v; wanted daily in jaffle_models with limit 1", others)
	}
	set := append([]wf.Workflow{groupedWorkflow("every_15", "jaffle_models", 3)}, others...)
	if err := wf.CheckGroups(set); err == nil {
		t.Error("publishing every_15 with limit 3 beside a published daily with limit 1 was accepted")
	}
}

// What this publish replaces is not "already published": republishing daily
// with a new limit is how the limit changes.
func TestAWorkflowBeingRepublishedIsNotItsOwnRival(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)
	p := project(t, pool, "p")
	if err := repo.Publicar(ctx, groupedWorkflow("daily", "jaffle_models", 1), p); err != nil {
		t.Fatal(err)
	}
	others, err := repo.PublishedGroupMembers(ctx, p, []string{"daily"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("the workflow being republished came back as a member: %+v", others)
	}
}

// --prune removes the project's workflows that are not in the files, so they
// are not members any more. Another project's are, prune or not.
func TestPruneLeavesOnlyOtherProjectsMembers(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)
	p, q := project(t, pool, "p"), project(t, pool, "q")
	if err := repo.Publicar(ctx, groupedWorkflow("daily", "jaffle_models", 1), p); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publicar(ctx, groupedWorkflow("theirs", "jaffle_models", 1), q); err != nil {
		t.Fatal(err)
	}
	others, err := repo.PublishedGroupMembers(ctx, p, []string{"every_15"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 1 || others[0].Slug != "theirs" {
		t.Errorf("with --prune, members = %+v; wanted only the other project's", others)
	}
}
