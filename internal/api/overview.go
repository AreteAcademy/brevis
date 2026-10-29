package api

import (
	"context"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
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
	if u.failing == nil {
		return pages.Attention{}
	}
	failing, err := u.freshFailing(ctx)
	if err != nil {
		u.log.Warn("the failing workflows are unavailable", "error", err)
		return pages.Attention{}
	}
	ordered := pages.OrderFailing(failing)
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
			}
		}
		out.Items = append(out.Items, it)
	}
	return out
}
