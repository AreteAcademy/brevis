package pages

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// AttentionShown is how many failing workflows "Needs your attention" draws.
// Each one's cause is a read of its last run's log, and a section that reads
// every log of a bad night is a page that takes a minute; past eight, the
// list says how many more there are and where.
const AttentionShown = 8

// Attention is what needs the reader (screen-overview): the workflows whose
// last finished run failed, the most pressing first.
type Attention struct {
	Items []AttentionItem
	// More is how many failing workflows are not drawn.
	More int
}

// Total is every failing workflow, drawn or not.
func (a Attention) Total() int { return len(a.Items) + a.More }

// AttentionItem is one workflow that failed: when, how many times in a row,
// and why -- read from that run's log, as its own page reads it.
type AttentionItem struct {
	Workflow string
	RunID    uuid.UUID
	FailedAt time.Time
	Streak   int
	Cause    *diagnosis.Cause
}

// OrderFailing puts what needs the reader first: a workflow failing again and
// again before one that failed once, and within each, the latest first.
func OrderFailing(fs []postgres.FailingWorkflow) []postgres.FailingWorkflow {
	out := append([]postgres.FailingWorkflow(nil), fs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].Streak >= 2, out[j].Streak >= 2
		if ri != rj {
			return ri
		}
		return out[i].FailedAt.After(out[j].FailedAt)
	})
	return out
}

// streakText says how many times in a row: nothing the first time, "2nd time
// in a row" the second -- the one ordinal every language here has -- and a
// count from the third on.
func streakText(ctx context.Context, n int) string {
	switch {
	case n < 2:
		return ""
	case n == 2:
		return i18n.T(ctx, "overview.second_time")
	}
	return i18n.N(ctx, "run.why.streak", n)
}
