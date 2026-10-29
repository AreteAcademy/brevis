package api

import (
	"context"
	"net/http"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
	"github.com/AreteAcademy/brevis/web/pages"
)

// causeOf reads why a run failed from its steps' logs, as the run's own page
// reads it: the Overview's "why" and the run page's must not disagree.
func causeOf(logs []postgres.StepLog, runErr string) *diagnosis.Cause {
	attempts := make([]diagnosis.StepAttempt, 0, len(logs))
	for _, l := range logs {
		attempts = append(attempts, diagnosis.StepAttempt{
			Step: l.NodeID, MapIndex: l.MapIndex, Attempt: l.Attempt, Status: l.Status,
			ExitCode: l.ExitCode, Err: l.Err, Log: l.Log, StartedAt: l.StartedAt,
		})
	}
	return diagnosis.Why(attempts, runErr)
}

// freshFailing reads the failing workflows past the badge's cache: the
// Overview lists them, and a workflow that just passed must leave the list
// at the next look, not fifteen seconds later.
func (u *UI) freshFailing(ctx context.Context) ([]postgres.FailingWorkflow, error) {
	if fresh, ok := u.failing.(interface {
		Fresh(context.Context) ([]postgres.FailingWorkflow, error)
	}); ok {
		return fresh.Fresh(ctx)
	}
	return u.failing.FailingWorkflows(ctx)
}

// attention is "Needs your attention": the failing workflows, the most
// pressing first, the first AttentionShown with why each one failed. A
// failure that cannot be read is an item without its why, not a page error:
// the section exists to be there on a bad night.
func (u *UI) attention(ctx context.Context) pages.Attention {
	var failing []postgres.FailingWorkflow
	if u.failing != nil {
		var err error
		if failing, err = u.freshFailing(ctx); err != nil {
			u.log.Warn("the failing workflows are unavailable", "error", err)
		}
	}
	out := u.failingItems(ctx, failing)
	out.Targets, out.TargetsMore = u.lateTargets(ctx, failing)
	return out
}

// failingItems are the failing workflows, the first AttentionShown with why.
func (u *UI) failingItems(ctx context.Context, failing []postgres.FailingWorkflow) pages.Attention {
	ordered := pages.OrderFailing(failing)
	if len(ordered) == 0 {
		return pages.Attention{}
	}
	rerunning := u.inFlightBySlug(ctx)
	var out pages.Attention
	for i, f := range ordered {
		if i == pages.AttentionShown {
			out.More = len(ordered) - i
			break
		}
		it := pages.AttentionItem{Workflow: f.Slug, RunID: f.RunID, FailedAt: f.FailedAt, Streak: f.Streak}
		if u.execs != nil {
			if r, err := u.execs.Get(ctx, f.RunID); err == nil {
				if logs, err := u.execs.LogsDaRun(ctx, f.RunID); err == nil {
					it.Cause = causeOf(logs, r.Err)
				}
				it.Params, it.Rerunnable = u.rerunValues(ctx, r.WorkflowSlug, r.Params)
			}
		}
		it.Rerunning = rerunning[f.Slug]
		out.Items = append(out.Items, it)
	}
	return out
}

// rerunValues are what Rerun sends: the failed run's values the workflow
// still declares -- the trigger refuses a name it no longer knows, and a
// rerun that answers 400 is worse than one that drops a dead value. A
// workflow that is gone cannot be rerun.
func (u *UI) rerunValues(ctx context.Context, slug string, used map[string]string) (map[string]string, bool) {
	if u.defs == nil || u.actions == nil {
		return nil, false
	}
	def, err := u.defs.Definition(ctx, slug)
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, p := range def.Params {
		if v, ok := used[p.Name]; ok {
			out[p.Name] = v
		}
	}
	return out, true
}

// inFlightBySlug is each workflow's run in flight, if any: a failing workflow
// whose rerun is going says so instead of offering another.
func (u *UI) inFlightBySlug(ctx context.Context) map[string]string {
	if u.leitura == nil {
		return nil
	}
	runs, err := u.leitura.InFlight(ctx, 100)
	if err != nil {
		u.log.Warn("the runs in flight are unavailable", "error", err)
		return nil
	}
	out := map[string]string{}
	for _, r := range runs {
		if _, seen := out[r.WorkflowSlug]; !seen {
			out[r.WorkflowSlug] = r.ID
		}
	}
	return out
}

// overviewAttention is "Needs your attention" alone, for the page to follow.
// In the reader's language and drawn directly: render would also take a
// flash meant for the next page.
func (u *UI) overviewAttention(w http.ResponseWriter, r *http.Request) {
	ctx := i18n.WithLang(r.Context(), i18n.Negotiate(r))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.AttentionLive(u.attention(ctx), time.Now()).Render(ctx, w); err != nil {
		u.log.Warn("rendering the attention fragment", "error", err)
	}
}

// lateTargets are the catalog's destinations whose data stopped coming,
// judged as /data judges them, the first AttentionShown and how many more.
// A catalog that cannot be read is a section without them.
func (u *UI) lateTargets(ctx context.Context, failing []postgres.FailingWorkflow) ([]pages.TargetItem, int) {
	if u.catalog == nil {
		return nil, 0
	}
	entries, err := u.catalog.Catalog(ctx)
	if err != nil {
		u.log.Warn("the catalog is unavailable", "error", err)
		return nil, 0
	}
	broken := make(map[string]bool, len(failing))
	for _, f := range failing {
		broken[f.Slug] = true
	}
	targets := pages.AttentionTargets(pages.BuildData(entries, time.Now()).Rows, broken)
	if len(targets) > pages.AttentionShown {
		return targets[:pages.AttentionShown], len(targets) - pages.AttentionShown
	}
	return targets, 0
}

// eventLog is the last FeedShown events. A log that cannot be read is an
// empty tab, not a page error: the rest of the Overview still answers.
func (u *UI) eventLog(ctx context.Context) []postgres.Event {
	if u.events == nil {
		return nil
	}
	evs, _, err := u.events.RecentEvents(ctx, postgres.EventQuery{Limit: pages.FeedShown})
	if err != nil {
		u.log.Warn("the event log is unavailable", "error", err)
		return nil
	}
	return evs
}
