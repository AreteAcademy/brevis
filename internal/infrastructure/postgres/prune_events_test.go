package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/events"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func eventsLeft(t *testing.T, pool *postgres.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// agedEvents writes two events older than a year and one from last week.
func agedEvents(t *testing.T, pool *postgres.Pool) {
	t.Helper()
	noEvents(t, pool)
	now := time.Now()
	happened(t, pool, now.Add(-400*24*time.Hour), events.WorkflowPublished, "old_a", "cli")
	happened(t, pool, now.Add(-500*24*time.Hour), events.SchedulePaused, "old_b", "ana")
	happened(t, pool, now.Add(-7*24*time.Hour), events.WorkflowPublished, "recent", "cli")
}

var yearly = postgres.Retention{TrimAfter: 30 * 24 * time.Hour, PurgeAfter: 365 * 24 * time.Hour, Batch: 1}

// THE FEED IS PURGED WITH THE RUNS: the events older than PurgeAfter go, in
// batches like the runs, and the report says how many.
func TestPruneDeletesTheEventsOlderThanTheRuns(t *testing.T) {
	pool := loadDB(t)
	agedEvents(t, pool)

	report, err := postgres.NewRunRepo(pool).Prune(context.Background(), yearly, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Events != 2 || eventsLeft(t, pool) != 1 {
		t.Errorf("report %s, %d events left; want 2 purged and 1 left", report, eventsLeft(t, pool))
	}
	if !strings.Contains(report.String(), "2 event(s)") {
		t.Errorf("the report does not count the events: %s", report)
	}
}

// --dry-run counts them and deletes none.
func TestADryPruneCountsTheEventsAndKeepsThem(t *testing.T) {
	pool := loadDB(t)
	agedEvents(t, pool)

	report, err := postgres.NewRunRepo(pool).Prune(context.Background(), yearly, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Events != 2 || eventsLeft(t, pool) != 3 {
		t.Errorf("report %s, %d events left; want 2 counted and 3 left", report, eventsLeft(t, pool))
	}
}

// PurgeAfter zero is never, for the events as for the runs.
func TestPurgeZeroKeepsEveryEvent(t *testing.T) {
	pool := loadDB(t)
	agedEvents(t, pool)

	report, err := postgres.NewRunRepo(pool).Prune(context.Background(),
		postgres.Retention{TrimAfter: 30 * 24 * time.Hour}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Events != 0 || eventsLeft(t, pool) != 3 {
		t.Errorf("report %s, %d events left; want none purged", report, eventsLeft(t, pool))
	}
}
