package postgres_test

import (
	"context"
	"testing"
	"time"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// A RETRY IS COUNTED IN ITS WINDOW (screen-overview-v2 OV1): a run on its
// second attempt or later healed a failure that does not show as one, and the
// Overview says how many -- inside the window it reads, not outside it.
func TestTheIndicatorsCountRetriesInTheWindow(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	retried := finished(t, pool, "daily", dom.StatusSuccess, time.Second)
	finished(t, pool, "daily", dom.StatusSuccess, time.Second)
	old := finished(t, pool, "daily", dom.StatusSuccess, time.Second)
	if _, err := pool.Exec(ctx, `UPDATE runs SET attempt = 1 WHERE id = $1`, retried); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runs SET attempt = 2, criado_em = now() - interval '3 days' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	reads := postgres.NewReadRepo(pool)

	day, err := reads.Indicators(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if day.Retried != 1 || day.Total != 2 {
		t.Errorf("24h: %d retried of %d, want 1 of 2", day.Retried, day.Total)
	}
	week, _ := reads.Indicators(ctx, 7*24*time.Hour)
	if week.Retried != 2 || week.Total != 3 {
		t.Errorf("7d: %d retried of %d, want 2 of 3", week.Retried, week.Total)
	}
}

// RUNS OVER A WINDOW, BY HOUR OR BY DAY: every bucket is there, the empty
// ones included, and a run lands in its own day.
func TestRunsAreBucketedByHourOrDay(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	finished(t, pool, "daily", dom.StatusSuccess, time.Second)
	twoDays := finished(t, pool, "daily", dom.StatusFailed, time.Second)
	if _, err := pool.Exec(ctx, `UPDATE runs SET criado_em = now() - interval '2 days' WHERE id = $1`, twoDays); err != nil {
		t.Fatal(err)
	}
	reads := postgres.NewReadRepo(pool)

	days, err := reads.RunsPer(ctx, postgres.ByDay, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 7 {
		t.Fatalf("%d day buckets, want 7", len(days))
	}
	if last := days[6]; last.Succeeded != 1 || last.Failed != 0 {
		t.Errorf("today = %+v", last)
	}
	if d := days[4]; d.Failed != 1 {
		t.Errorf("two days ago = %+v", d)
	}
	hours, err := reads.RunsPer(ctx, postgres.ByHour, 24)
	if err != nil || len(hours) != 24 || hours[23].Succeeded != 1 {
		t.Errorf("hours: %d buckets, last %+v, err %v", len(hours), hours[len(hours)-1], err)
	}
}

// THE CHART'S WINDOW BOUNDS THE SCAN: a run older than the window lands in no
// bucket (and the join no longer reads it), and the buckets are UTC days
// whatever the session's time zone.
func TestRunsPerIgnoresRunsBeforeTheWindowAndBucketsInUTC(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	finished(t, pool, "daily", dom.StatusSuccess, time.Second)
	old := finished(t, pool, "daily", dom.StatusFailed, time.Second)
	if _, err := pool.Exec(ctx, `UPDATE runs SET criado_em = now() - interval '40 days' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	reads := postgres.NewReadRepo(pool)

	days, err := reads.RunsPer(ctx, postgres.ByDay, 7)
	if err != nil {
		t.Fatal(err)
	}
	var ok, failed int
	for _, d := range days {
		ok, failed = ok+d.Succeeded, failed+d.Failed
	}
	if len(days) != 7 || ok != 1 || failed != 0 {
		t.Errorf("%d buckets with %d succeeded, %d failed; want 7 with 1 and 0", len(days), ok, failed)
	}
	if want := time.Now().UTC().Truncate(24 * time.Hour); !days[6].Start.UTC().Equal(want) {
		t.Errorf("last bucket starts %s, want the UTC day %s", days[6].Start.UTC(), want)
	}
}
