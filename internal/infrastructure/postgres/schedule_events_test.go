package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func scheduled(t *testing.T, pool *postgres.Pool, slug string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO schedules (id, workflow_slug, cron) VALUES (gen_random_uuid(), $1, '0 4 * * *')`, slug); err != nil {
		t.Fatal(err)
	}
}

// recorded is every event, oldest first, as "kind subject actor".
func recorded(t *testing.T, pool *postgres.Pool) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT kind, subject, actor FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k, s, a string
		if err := rows.Scan(&k, &s, &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.TrimSpace(k+" "+s+" "+a))
	}
	return strings.Join(out, ", ")
}

// PAUSING AND RESUMING ARE IN THE FEED, with who did it: the toggle writes
// the event in the same statement that flips the schedule.
func TestTogglingAScheduleRecordsWhoPausedAndResumedIt(t *testing.T) {
	pool := insightsDB(t)
	scheduled(t, pool, "daily")
	repo := postgres.NewScheduleRepo(pool)
	ctx := context.Background()

	if on, err := repo.Toggle(ctx, "daily", "ana"); err != nil || on {
		t.Fatalf("Toggle = %v, %v; want paused", on, err)
	}
	if on, err := repo.Toggle(ctx, "daily", ""); err != nil || !on {
		t.Fatalf("Toggle = %v, %v; want resumed", on, err)
	}
	if got := recorded(t, pool); got != "schedule.paused daily ana, schedule.resumed daily" {
		t.Errorf("events = %s", got)
	}
}

// SETTING A SCHEDULE TO THE STATE IT IS IN changes nothing, so it says
// nothing: the feed is what changed.
func TestSettingAScheduleToItsStateRecordsNothing(t *testing.T) {
	pool := insightsDB(t)
	scheduled(t, pool, "daily")
	repo := postgres.NewScheduleRepo(pool)
	ctx := context.Background()

	for _, active := range []bool{true, false, false, true} {
		if got, err := repo.SetActive(ctx, "daily", active, "cli"); err != nil || got != active {
			t.Fatalf("SetActive(%v) = %v, %v", active, got, err)
		}
	}
	if got := recorded(t, pool); got != "schedule.paused daily cli, schedule.resumed daily cli" {
		t.Errorf("events = %s", got)
	}
}

// A SCHEDULE THAT DOES NOT EXIST is an error, and leaves no event behind.
func TestTogglingNoScheduleRecordsNothing(t *testing.T) {
	pool := insightsDB(t)
	if _, err := postgres.NewScheduleRepo(pool).Toggle(context.Background(), "ghost", "ana"); err == nil {
		t.Error("toggling a schedule that does not exist did not fail")
	}
	if got := recorded(t, pool); got != "" {
		t.Errorf("events = %s; want none", got)
	}
}

// TEN OPERATORS CLICKING AT ONCE are ten changes and ten events, half of
// each, and the schedule ends where it started: the UPDATE decides what
// changed, under the row's lock, not a read that two clicks would share.
func TestConcurrentTogglesEachRecordTheirChange(t *testing.T) {
	pool := insightsDB(t)
	scheduled(t, pool, "daily")
	repo := postgres.NewScheduleRepo(pool)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := repo.Toggle(ctx, "daily", "ana"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	var paused, resumed int
	var on bool
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE kind = 'schedule.paused'), count(*) FILTER (WHERE kind = 'schedule.resumed'),
		       (SELECT ativo FROM schedules WHERE workflow_slug = 'daily')
		FROM events`).Scan(&paused, &resumed, &on); err != nil {
		t.Fatal(err)
	}
	if paused != 5 || resumed != 5 || !on {
		t.Errorf("paused %d, resumed %d, active %v; want 5, 5 and active", paused, resumed, on)
	}
}
