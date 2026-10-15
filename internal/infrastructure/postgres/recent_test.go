package postgres_test

import (
	"context"
	"testing"
	"time"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// THE LAST N RUNS OF EACH WORKFLOW ON A PAGE, IN ONE QUERY: oldest to newest,
// so the strip reads left to right like time, and only for the slugs asked.
func TestRecentStatesAreEachWorkflowsLastRunsOldestFirst(t *testing.T) {
	pool := insightsDB(t)
	for i := 16; i >= 1; i-- { // sixteen runs, the newest last
		status := dom.StatusSuccess
		if i == 1 {
			status = dom.StatusFailed
		}
		finished(t, pool, "busy", status, time.Duration(i)*time.Hour)
	}
	finished(t, pool, "quiet", dom.StatusSuccess, 2*time.Hour)
	finished(t, pool, "quiet", dom.StatusFailed, time.Hour)
	finished(t, pool, "not_asked", dom.StatusSuccess, time.Hour)

	got, err := postgres.NewReadRepo(pool).RecentStates(context.Background(), []string{"busy", "quiet", "never_ran"}, 14)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got["busy"]); n != 14 {
		t.Fatalf("busy has %d recent runs, want 14", n)
	}
	if last := got["busy"][13]; last.Status != "failed" {
		t.Errorf("the newest run is not last: %+v", last)
	}
	for i := 1; i < 14; i++ {
		if got["busy"][i].At.Before(got["busy"][i-1].At) {
			t.Fatal("the strip is not oldest to newest")
		}
	}
	if q := got["quiet"]; len(q) != 2 || q[0].Status != "success" || q[1].Status != "failed" || q[1].ID == "" {
		t.Errorf("quiet = %+v", q)
	}
	if _, ok := got["not_asked"]; ok {
		t.Error("a workflow off the page was read")
	}
	if len(got["never_ran"]) != 0 {
		t.Error("a workflow that never ran has runs")
	}
}
