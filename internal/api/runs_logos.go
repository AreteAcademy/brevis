package api

import (
	"context"
	"slices"

	"github.com/AreteAcademy/brevis/internal/domain/runtimes"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/pages"
)

// logosShown is how many marks a Runs item carries: what a workflow runs in
// reads at a glance up to two, and a third is a row of icons.
const logosShown = 2

// workflowLogos is what a workflow runs in, as logo keys: each step's tools
// before its language -- `dbt build` is a dbt workflow before it is a Python
// one -- in the order of the steps, two at most. Inferred on read, like the
// graph's chips, so fixing a rule fixes every run's item.
func workflowLogos(def wf.Workflow) []string {
	var keys []string
	add := func(k string) {
		if k != "" && len(keys) < logosShown && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, no := range def.Nodes {
		tc := runtimes.Resolve(no.Runtime, no.Tools, runtimes.Detect(no.Run, def.ImageFor(no), no.Action))
		for _, t := range tc.Tools {
			add(t)
		}
		add(tc.Runtime)
	}
	return keys
}

// logosOf is what each of these workflows runs in, by slug. From the CURRENT
// definition, not the run's: a workflow that changed what it runs in shows the
// new mark on its old runs, which is the price of not storing it per run.
//
// One workflow reads its definition alone; more read every definition in one
// query, rather than one per workflow on the page. A workflow that is gone, or
// fails to read, has no marks -- not a page error.
func (u *UI) logosOf(ctx context.Context, slugs []string) map[string][]string {
	if u.defs == nil || len(slugs) == 0 {
		return nil
	}
	out := map[string][]string{}
	if len(slugs) == 1 {
		if def, err := u.defs.Definition(ctx, slugs[0]); err == nil {
			out[slugs[0]] = workflowLogos(def)
		}
		return out
	}
	defs, err := u.defs.Definitions(ctx)
	if err != nil {
		u.log.Error("reading the definitions for the logos", "error", err)
		return out
	}
	for _, slug := range slugs {
		if def, ok := defs[slug]; ok {
			out[slug] = workflowLogos(def)
		}
	}
	return out
}

// distinct is each slug once, in the order first seen.
func distinct(slugs []string) []string {
	var out []string
	for _, s := range slugs {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// runsLogos is what the workflow of each run on the page runs in.
func (u *UI) runsLogos(ctx context.Context, runs []postgres.RunSummary) map[string][]string {
	slugs := make([]string, len(runs))
	for i, r := range runs {
		slugs[i] = r.WorkflowSlug
	}
	return u.logosOf(ctx, distinct(slugs))
}

// withLogos gives each next trigger what its workflow runs in.
func (u *UI) withLogos(ctx context.Context, next []pages.NextRun) []pages.NextRun {
	slugs := make([]string, len(next))
	for i, n := range next {
		slugs[i] = n.Workflow
	}
	logos := u.logosOf(ctx, distinct(slugs))
	for i, n := range next {
		next[i].Logos = logos[n.Workflow]
	}
	return next
}
