package api

import (
	"context"
	"slices"

	"github.com/AreteAcademy/brevis/internal/domain/runtimes"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
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

// runsLogos reads each workflow on the page once. From the CURRENT
// definition, not the run's: a workflow that changed what it runs in shows
// the new mark on its old runs, which is the price of not storing it per run.
// A workflow that is gone, or fails to read, has no marks -- not a page error.
func (u *UI) runsLogos(ctx context.Context, runs []postgres.RunSummary) map[string][]string {
	if u.defs == nil {
		return nil
	}
	out := map[string][]string{}
	for _, r := range runs {
		if _, done := out[r.WorkflowSlug]; done {
			continue
		}
		out[r.WorkflowSlug] = nil
		if def, err := u.defs.Definition(ctx, r.WorkflowSlug); err == nil {
			out[r.WorkflowSlug] = workflowLogos(def)
		}
	}
	return out
}
