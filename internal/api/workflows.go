package api

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/pages"
)

// listFacts is what the Workflows list knows beyond each row's summary: the
// published definitions (what each workflow runs, and its commands) and who
// is failing. Read once per request, before filtering, because the filters
// need them for every workflow and not only the page's.
type listFacts struct {
	defs map[string]wf.Workflow
	// failing is who the sidebar's badge counts; nil when it could not be
	// read, and then the last run's state answers instead.
	failing map[string]bool
}

func (u *UI) listFacts(ctx context.Context) listFacts {
	var x listFacts
	if u.defs != nil {
		defs, err := u.defs.Definitions(ctx)
		if err != nil {
			u.log.Error("reading the definitions for the Workflows list", "error", err)
		}
		x.defs = defs
	}
	if u.failing != nil {
		ws, err := u.failing.FailingWorkflows(ctx)
		if err != nil {
			u.log.Error("reading the failing workflows for the Workflows list", "error", err)
		} else {
			x.failing = map[string]bool{}
			for _, w := range ws {
				x.failing[w.Slug] = true
			}
		}
	}
	return x
}

// inState is whether a workflow answers the state's question. The segment's
// five: failing is the last FINISHED run failing -- a failed workflow with a
// retry queued is still failing -- and running is anything in flight. Any
// other state is an older link's, the last run's state as it was.
func (x listFacts) inState(w postgres.WorkflowSummary, state string) bool {
	switch state {
	case "":
		return true
	case "failing":
		if x.failing == nil {
			return w.LastStatus == "failed"
		}
		return x.failing[w.Slug]
	case "running":
		return w.LastStatus == "running" || w.LastStatus == "queued" || w.LastStatus == "retrying"
	case "paused":
		return w.HasSchedule && !w.Active
	case "manual":
		return !w.HasSchedule
	}
	return w.LastStatus == state
}

// haystack is what the search looks in: the name, the tags, and what the
// steps run -- "dbt" finds the workflows that run it.
func (x listFacts) haystack(w postgres.WorkflowSummary) string {
	parts := append([]string{w.Slug, w.Name}, w.Tags...)
	for _, n := range x.defs[w.Slug].Nodes {
		parts = append(parts, n.Run, n.Action)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// langs is every language and tool a workflow runs, as logo keys.
func (x listFacts) langs(slug string) []string {
	def, ok := x.defs[slug]
	if !ok {
		return nil
	}
	return runsIn(def, 0)
}

// langChoices are the languages the workflows run, each with how many the
// rest of the filter keeps: most used first. A language the filter leaves at
// zero stays, so the control does not move under the pointer.
func langChoices(all []postgres.WorkflowSummary, f pages.Filter, x listFacts) []pages.LangChoice {
	g := f
	g.Lang = ""
	kept := map[string]bool{}
	for _, w := range filtrar(all, g, x) {
		kept[w.Slug] = true
	}
	counts := map[string]int{}
	for _, w := range all {
		for _, k := range x.langs(w.Slug) {
			if _, seen := counts[k]; !seen {
				counts[k] = 0
			}
			if kept[w.Slug] {
				counts[k]++
			}
		}
	}
	out := make([]pages.LangChoice, 0, len(counts))
	for k, n := range counts {
		out = append(out, pages.LangChoice{Key: k, Count: n})
	}
	slices.SortFunc(out, func(a, b pages.LangChoice) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// validListState is a state the list filters by: the segment's questions,
// or a run state an older link carries.
func validListState(s string) string {
	if pages.IsWorkflowState(s) {
		return s
	}
	return validState(s)
}

var logoKey = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// dayOf is a `day=` parameter as a UTC day, or nil when it is not one.
func dayOf(s string) *time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &d
}

// validLang is a logo key, or nothing.
func validLang(s string) string {
	if logoKey.MatchString(s) {
		return s
	}
	return ""
}

// connectsWindow is how far back what a workflow wrote counts as what it
// talks to: a destination it stopped writing to three months ago is history.
const connectsWindow = 90 * 24 * time.Hour

// describeRows fills what each row of the page says beyond its summary: what
// it runs, what it talks to, how many steps, whether it is failing.
func (u *UI) describeRows(ctx context.Context, v *pages.WorkflowsScreen, x listFacts) {
	wrote, err := u.leitura.Connects(ctx, time.Now().Add(-connectsWindow))
	if err != nil {
		u.log.Error("reading what the workflows wrote", "error", err)
	}
	v.RunsIn, v.Connects, v.Steps = map[string][]string{}, map[string][]string{}, map[string]int{}
	v.Failing, v.Params, v.Now = map[string]bool{}, map[string]bool{}, time.Now()
	for _, w := range v.Rows {
		v.Failing[w.Slug] = x.inState(w, "failing")
		def, ok := x.defs[w.Slug]
		if !ok {
			continue
		}
		v.RunsIn[w.Slug] = runsIn(def, 0)
		v.Steps[w.Slug] = len(def.Nodes)
		v.Params[w.Slug] = len(def.Params) > 0
		v.Connects[w.Slug] = talksTo(def, wrote[w.Slug], v.RunsIn[w.Slug])
	}
}

// talksTo is what a workflow talks to, as logo keys: the schemes it wrote,
// then what its parameters say they bill and what lands to start it -- once
// each, and never a language or tool it runs, which its own column says.
func talksTo(def wf.Workflow, wrote, runs []string) []string {
	var out []string
	add := func(k string) {
		k = connectorKey(k)
		if k != "" && !slices.Contains(out, k) && !slices.Contains(runs, k) {
			out = append(out, k)
		}
	}
	for _, s := range wrote {
		add(s)
	}
	for _, p := range def.Params {
		add(p.Uses)
	}
	for _, t := range def.Trigger.OnLanded {
		if scheme, _, ok := strings.Cut(t, "://"); ok {
			add(scheme)
		}
	}
	return out
}

// schemeKeys are the URL schemes whose logo has another name.
var schemeKeys = map[string]string{
	"gs": "gcs", "pg": "postgres", "postgresql": "postgres", "s3a": "s3", "s3n": "s3",
	"https": "http", "bq": "bigquery",
}

// connectorKey is the logo key of a scheme or a declared `uses`.
func connectorKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if k, ok := schemeKeys[s]; ok {
		return k
	}
	return s
}
