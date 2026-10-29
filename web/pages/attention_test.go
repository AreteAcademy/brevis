package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// A workflow failing again and again comes before one that failed once, and
// within each the latest first: the one most likely to need a person now.
func TestFailingIsOrderedByWhatNeedsAPerson(t *testing.T) {
	now := time.Now()
	got := OrderFailing([]postgres.FailingWorkflow{
		{Slug: "once_old", Streak: 1, FailedAt: now.Add(-3 * time.Hour)},
		{Slug: "again_old", Streak: 4, FailedAt: now.Add(-2 * time.Hour)},
		{Slug: "once_new", Streak: 1, FailedAt: now.Add(-time.Minute)},
		{Slug: "again_new", Streak: 2, FailedAt: now.Add(-time.Hour)},
	})
	var order []string
	for _, f := range got {
		order = append(order, f.Slug)
	}
	if strings.Join(order, ",") != "again_new,again_old,once_new,once_old" {
		t.Errorf("ordered %v", order)
	}
}

// Nothing the first time, the ordinal the second, a count from the third.
func TestTheStreakIsSaidAsAReaderCountsIt(t *testing.T) {
	ctx := context.Background()
	for n, want := range map[int]string{0: "", 1: "", 2: "2nd time in a row", 3: "Failed 3 times in a row"} {
		if got := streakText(ctx, n); got != want {
			t.Errorf("streak %d says %q; wanted %q", n, got, want)
		}
	}
}

func row(target string, status catalog.Status, workflow string) DataRow {
	missed := time.Now().Add(-90 * time.Minute)
	return DataRow{
		CatalogEntry: postgres.CatalogEntry{Target: target},
		Status:       status,
		Writers: []WriterView{{
			CatalogWriter: postgres.CatalogWriter{Workflow: workflow},
			Verdict:       catalog.Verdict{Status: status, Missed: missed},
		}},
	}
}

// A DESTINATION NEEDS YOU WHEN ITS DATA STOPPED COMING: stale before late,
// what is on time not at all -- and not when the workflow that writes it is
// already in the list, failing: that is why it is late, and saying it twice
// is noise.
func TestTheTargetsThatNeedYou(t *testing.T) {
	got := AttentionTargets([]DataRow{
		row("bq.late_one", catalog.Late, "a"),
		row("bq.fine", catalog.OnTime, "b"),
		row("bq.stale_one", catalog.Stale, "c"),
		row("bq.because_failing", catalog.Stale, "broken"),
	}, map[string]bool{"broken": true})
	var order []string
	for _, it := range got {
		order = append(order, it.Target)
	}
	if strings.Join(order, ",") != "bq.stale_one,bq.late_one" {
		t.Errorf("targets needing attention: %v", order)
	}
	if got[0].Workflow != "c" || got[0].Missed.IsZero() || got[0].Status != catalog.Stale {
		t.Errorf("the stale target lost its writer or its missed slot: %+v", got[0])
	}
}
