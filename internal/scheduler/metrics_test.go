package scheduler_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/observability/metrics"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

func scrapeOf(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.WriteTo(context.Background(), &b); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	return b.String()
}

// TestARunThatRetriedIsCountedOnce.
//
// The dispatcher passes through process() once per ATTEMPT, so the obvious
// place to count a run is the one that reports three where the operator saw
// one. This is the assertion that keeps brevis_run_total meaning "runs" -- and
// it is why the counter lives at the terminal state and not in process().
func TestARunThatRetriedIsCountedOnce(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "counted-once",
		TriggerType: "schedule", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	m := metrics.New()
	var calls int32
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 3,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, q, repo, func(context.Context, uuid.UUID) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New(`step "run": exited with code 2`)
		}
		return nil
	}, noLog())
	d.Metrics = m

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool {
		current, _ := repo.Get(context.Background(), r.ID)
		return current.Status == dom.StatusSuccess
	})
	cancel()
	time.Sleep(150 * time.Millisecond)

	text := scrapeOf(t, m)
	want := `brevis_run_total{status="success",trigger="schedule",workflow="id_verification"} 1`
	if !strings.Contains(text, want+"\n") {
		t.Errorf("missing: %s\n--- the scrape said ---\n%s", want, text)
	}
	// And the failed attempt did not become a failed RUN. The step-level
	// numbers are where a failed attempt shows.
	if strings.Contains(text, `brevis_run_total{status="failed"`) {
		t.Errorf("a retried run was counted as a failure too:\n%s", text)
	}
}

// TestTheClaimLatencyIsMeasured. Enqueued to claimed is the scheduler's own
// SLA, and it is the first number anybody wants when pipelines "feel slow".
func TestTheClaimLatencyIsMeasured(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "latency", TriggerType: "manual",
		Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	m := metrics.New()
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, Interval: 10 * time.Millisecond,
	}, q, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())
	d.Metrics = m

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool {
		current, _ := repo.Get(context.Background(), r.ID)
		return current.Status == dom.StatusSuccess
	})
	cancel()
	time.Sleep(150 * time.Millisecond)

	text := scrapeOf(t, m)
	if !strings.Contains(text, "brevis_claim_latency_seconds_count 1\n") {
		t.Errorf("the claim was not measured:\n%s", text)
	}
}

// TestAnOrphanSweepIsCounted. A worker dying was a log line and nothing else,
// so "workers are dying" was not something anybody could alert on.
func TestAnOrphanSweepIsCounted(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "orphan-metric", TriggerType: "manual",
		Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Claimed by a worker that then dies: the item stays held and nothing
	// releases it.
	if _, err := q.Claim(ctx, "the-worker-that-died", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}

	m := metrics.New()
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1,
		// A visibility of zero makes every claimed item an orphan at once,
		// which is what turns a fifteen-minute timeout into a test.
		Visibility: time.Nanosecond,
	}, q, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())
	d.Metrics = m

	n, err := d.RecoverOrphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered %d orphans, wanted 1", n)
	}

	text := scrapeOf(t, m)
	if !strings.Contains(text, "brevis_orphans_recovered_total 1\n") {
		t.Errorf("the sweep was not counted:\n%s", text)
	}
}

// TestTheQueueDepthComesFromTheTable. The depth is read at collect time and not
// counted locally, because the table is shared: several dispatchers write to
// it, and a local counter would report this replica's opinion of a number that
// belongs to the queue.
func TestTheQueueDepthComesFromTheTable(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	for i := range 3 {
		r, err := repo.Create(ctx, dom.Run{
			WorkflowSlug: "w", IdempotencyKey: "depth-" + string(rune('a'+i)),
			TriggerType: "manual", Definition: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := q.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Claim(ctx, "someone", 1); err != nil {
		t.Fatal(err)
	}

	m := metrics.New()
	if err := m.WatchQueue(q.Size); err != nil {
		t.Fatal(err)
	}

	text := scrapeOf(t, m)
	for _, want := range []string{
		`brevis_queue_depth{state="claimed"} 1`,
		`brevis_queue_depth{state="pending"} 2`,
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("missing: %s\n--- the scrape said ---\n%s", want, text)
		}
	}
}

// waitFor polls until the condition holds or the test's patience runs out.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run never reached the state the test needs")
}
