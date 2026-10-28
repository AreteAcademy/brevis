package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/events"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// happened writes an event the way an action will, at a chosen instant.
func happened(t *testing.T, pool *postgres.Pool, at time.Time, kind events.Kind, subject, actor string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO events (at, kind, subject, actor, detail) VALUES ($1, $2, $3, $4, '{"n": 1}')`,
		at, kind, subject, actor); err != nil {
		t.Fatal(err)
	}
}

// ran writes a run that started, and finished when finished is not zero.
func ran(t *testing.T, pool *postgres.Pool, slug string, status dom.Status, started, ended time.Time) uuid.UUID {
	t.Helper()
	id := finished(t, pool, slug, status, time.Hour)
	var end *time.Time
	if !ended.IsZero() {
		end = &ended
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE runs SET iniciado_em = $2, terminado_em = $3 WHERE id = $1`, id, started, end); err != nil {
		t.Fatal(err)
	}
	return id
}

func kinds(evs []postgres.Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, string(e.Kind)+" "+e.Subject)
	}
	return strings.Join(out, ", ")
}

var now0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// THE FEED IS WHAT HAPPENED, NEWEST FIRST: what somebody did and what ran,
// interleaved by when. A run is there when it started and again when it
// ended; one still in the queue has not happened yet.
func TestTheFeedInterleavesEventsAndRunsNewestFirst(t *testing.T) {
	pool := insightsDB(t)
	happened(t, pool, now0.Add(-5*time.Minute), events.WorkflowPublished, "daily", "cli")
	a := ran(t, pool, "daily", dom.StatusSuccess, now0.Add(-4*time.Minute), now0.Add(-3*time.Minute))
	happened(t, pool, now0.Add(-2*time.Minute), events.SchedulePaused, "daily", "ana")
	b := ran(t, pool, "daily", dom.StatusRunning, now0.Add(-time.Minute), time.Time{})
	if _, err := pool.Exec(context.Background(), `UPDATE runs SET status = 'queued', iniciado_em = NULL, terminado_em = NULL
		WHERE id = $1`, finished(t, pool, "daily", dom.StatusQueued, time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, next, err := postgres.NewEventRepo(pool).RecentEvents(context.Background(), postgres.EventQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := "run.started daily, schedule.paused daily, run.succeeded daily, run.started daily, workflow.published daily"
	if kinds(got) != want {
		t.Fatalf("the feed = %s\nwant       %s", kinds(got), want)
	}
	if next != "" {
		t.Errorf("a short page has a next cursor %q", next)
	}
	if got[0].RunID == nil || *got[0].RunID != b || got[0].Trigger != "schedule" || !got[0].At.Equal(now0.Add(-time.Minute)) {
		t.Errorf("the running run's start = %+v", got[0])
	}
	if got[2].RunID == nil || *got[2].RunID != a {
		t.Errorf("the finished run = %+v", got[2])
	}
	if got[1].Actor != "ana" || got[1].RunID != nil || got[1].Detail["n"] != float64(1) {
		t.Errorf("the pause = %+v", got[1])
	}
}

// PAGES DO NOT REPEAT OR SKIP, even when several things happened in the same
// instant: the cursor is the instant, the branch and an id, so the order is
// fixed whatever the page size.
func TestThePagesOfTheFeedNeitherRepeatNorSkip(t *testing.T) {
	pool := insightsDB(t)
	same := now0.Add(-time.Minute)
	for i := range 4 {
		happened(t, pool, same, events.StreamPublished, fmt.Sprintf("g/s%d", i), "cli")
	}
	ran(t, pool, "tied", dom.StatusFailed, same, same)
	ran(t, pool, "tied2", dom.StatusSuccess, same, same)
	for i := range 5 {
		happened(t, pool, now0.Add(-time.Duration(i+2)*time.Minute), events.WorkflowPublished, fmt.Sprintf("w%d", i), "cli")
	}
	repo := postgres.NewEventRepo(pool)
	ctx := context.Background()

	whole, _, err := repo.RecentEvents(ctx, postgres.EventQuery{Limit: 100})
	if err != nil || len(whole) != 13 {
		t.Fatalf("one page = %d events, %v; want 13", len(whole), err)
	}
	for _, size := range []int{1, 2, 3, 5} {
		var paged []postgres.Event
		cursor := ""
		for range 20 {
			page, next, err := repo.RecentEvents(ctx, postgres.EventQuery{Before: cursor, Limit: size})
			if err != nil {
				t.Fatal(err)
			}
			paged = append(paged, page...)
			if next == "" {
				break
			}
			cursor = next
		}
		if kinds(paged) != kinds(whole) {
			t.Errorf("pages of %d =\n%s\nwant\n%s", size, kinds(paged), kinds(whole))
		}
	}
}

// THE TAB FILTERS BY SOURCE: what a workflow did and ran, what streams
// published, what endpoints did.
func TestTheFeedFiltersBySource(t *testing.T) {
	pool := insightsDB(t)
	happened(t, pool, now0.Add(-4*time.Minute), events.WorkflowPublished, "daily", "cli")
	happened(t, pool, now0.Add(-3*time.Minute), events.StreamPublished, "g/orders", "cli")
	happened(t, pool, now0.Add(-2*time.Minute), events.EndpointsPublished, "metrics", "cli")
	ran(t, pool, "daily", dom.StatusFailed, now0.Add(-90*time.Second), now0.Add(-time.Minute))
	repo := postgres.NewEventRepo(pool)

	for source, want := range map[events.Source]string{
		events.FromWorkflow: "run.failed daily, run.started daily, workflow.published daily",
		events.FromStream:   "stream.published g/orders",
		events.FromEndpoint: "endpoints.published metrics",
	} {
		got, _, err := repo.RecentEvents(context.Background(), postgres.EventQuery{Source: source})
		if err != nil || kinds(got) != want {
			t.Errorf("%s: %s, %v; want %s", source, kinds(got), err, want)
		}
	}
	if _, _, err := repo.RecentEvents(context.Background(), postgres.EventQuery{Source: "billing"}); err == nil {
		t.Error("an unknown source was accepted")
	}
}

// A PAGE HAS A CEILING: 200 at most, 50 when none is asked.
func TestAPageOfTheFeedIsBounded(t *testing.T) {
	pool := insightsDB(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO events (at, kind, subject)
		SELECT now() - g * interval '1 second', 'workflow.published', 'w' || g FROM generate_series(1, 250) g`); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewEventRepo(pool)
	for limit, want := range map[int]int{0: 50, 500: 200, 10: 10} {
		got, next, err := repo.RecentEvents(context.Background(), postgres.EventQuery{Limit: limit})
		if err != nil || len(got) != want || next == "" {
			t.Errorf("limit %d: %d events, next %q, %v; want %d and a next page", limit, len(got), next, err, want)
		}
	}
}

// A KIND THIS VERSION DOES NOT KNOW is left out -- and left out by the query,
// so it does not make a page short.
func TestAnUnknownKindIsLeftOutOfTheFeed(t *testing.T) {
	pool := insightsDB(t)
	for i := range 3 {
		happened(t, pool, now0.Add(-time.Duration(i+2)*time.Minute), events.WorkflowPublished, fmt.Sprintf("w%d", i), "cli")
	}
	happened(t, pool, now0.Add(-time.Minute), "cache.generated", "metrics", "")
	got, _, err := postgres.NewEventRepo(pool).RecentEvents(context.Background(), postgres.EventQuery{Limit: 3})
	if err != nil || kinds(got) != "workflow.published w0, workflow.published w1, workflow.published w2" {
		t.Errorf("the feed = %s, %v", kinds(got), err)
	}
}

// A CURSOR THE FEED DID NOT WRITE is refused, not read as "from the start".
func TestABadCursorIsRefused(t *testing.T) {
	pool := insightsDB(t)
	if _, _, err := postgres.NewEventRepo(pool).RecentEvents(context.Background(), postgres.EventQuery{Before: "not-a-cursor"}); err == nil {
		t.Error("a bad cursor was accepted")
	}
}

// EVERY BRANCH READS ITS INDEX: the feed reads runs across every workflow,
// and a page must not cost a scan of the whole table, with or without a
// cursor and whatever the filter.
func TestEveryBranchOfTheFeedReadsAnIndex(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	happened(t, pool, now0, events.WorkflowPublished, "daily", "cli")
	_, cursor, err := postgres.NewEventRepo(pool).RecentEvents(ctx, postgres.EventQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]postgres.EventQuery{
		"everything":     {Limit: 50},
		"after a cursor": {Limit: 50, Before: cursor},
		"workflows":      {Limit: 50, Source: events.FromWorkflow},
		"streams":        {Limit: 50, Source: events.FromStream},
	} {
		t.Run(name, func(t *testing.T) {
			sql, args, err := postgres.RecentEventsQuery(q)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, line)
			}
			rows.Close()
			joined := strings.Join(plan, "\n")
			if strings.Contains(joined, "Seq Scan") {
				t.Errorf("the plan scans:\n%s", joined)
			}
			indexes := []string{"events_at_idx", "runs_started_idx", "runs_finished_idx"}
			if q.Source == events.FromStream {
				indexes = indexes[:1]
			}
			for _, idx := range indexes {
				if !strings.Contains(joined, idx) {
					t.Errorf("the plan does not read %s:\n%s", idx, joined)
				}
			}
		})
	}
}

// THE FEED'S PRICE, per page of 50: a year of an installation with a million
// runs over a thousand workflows and a hundred thousand events. Run with
// -bench; `make test-int` does not.
func BenchmarkRecentEvents(b *testing.B) {
	pool := insightsDB(b)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO runs (id, workflow_slug, idempotency_key, status, definicao, trigger_type,
		                  criado_em, iniciado_em, terminado_em)
		SELECT gen_random_uuid(), 'wf_' || (g % 1000), 'bench-' || g,
		       CASE WHEN g % 20 = 0 THEN 'failed' WHEN g % 97 = 0 THEN 'canceled' ELSE 'success' END,
		       '{}', 'schedule',
		       now() - g * interval '30 seconds', now() - g * interval '30 seconds',
		       now() - g * interval '30 seconds' + interval '2 minutes'
		FROM generate_series(1, 1000000) g;
		INSERT INTO events (at, kind, subject, actor)
		SELECT now() - g * interval '5 minutes',
		       (ARRAY['workflow.published', 'schedule.paused', 'stream.published', 'endpoints.published'])[g % 4 + 1],
		       'subject_' || (g % 500), 'cli'
		FROM generate_series(1, 100000) g;
		ANALYZE runs; ANALYZE events`); err != nil {
		b.Fatal(err)
	}
	repo := postgres.NewEventRepo(pool)
	_, cursor, err := repo.RecentEvents(ctx, postgres.EventQuery{Limit: 50})
	if err != nil {
		b.Fatal(err)
	}
	for name, q := range map[string]postgres.EventQuery{
		"everything":     {Limit: 50},
		"after a cursor": {Limit: 50, Before: cursor},
		"workflows":      {Limit: 50, Source: events.FromWorkflow},
		"streams":        {Limit: 50, Source: events.FromStream},
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, _, err := repo.RecentEvents(ctx, q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
