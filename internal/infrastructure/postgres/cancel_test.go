package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/events"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// runAt makes a run and walks it to `to`, through the states the dispatcher
// would take it through.
func runAt(t *testing.T, repo *postgres.RunRepo, to dom.Status) uuid.UUID {
	t.Helper()
	id := newRun(t, repo)
	path := map[dom.Status][]dom.Status{
		dom.StatusCreated:  nil,
		dom.StatusQueued:   {dom.StatusQueued},
		dom.StatusRunning:  {dom.StatusQueued, dom.StatusRunning},
		dom.StatusSuccess:  {dom.StatusQueued, dom.StatusRunning, dom.StatusSuccess},
		dom.StatusFailed:   {dom.StatusQueued, dom.StatusRunning, dom.StatusFailed},
		dom.StatusRetrying: {dom.StatusQueued, dom.StatusRunning, dom.StatusFailed, dom.StatusRetrying},
	}[to]
	for _, s := range path {
		if err := repo.Transicionar(context.Background(), id, s); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// enqueued puts the run's item in the queue, claimed or not, as the scheduler
// and a dispatcher would leave it.
func enqueued(t *testing.T, pool *postgres.Pool, id uuid.UUID, claimed bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO queue_items (run_id, reivindicado_em, reivindicado_por)
		VALUES ($1, CASE WHEN $2 THEN now() END, CASE WHEN $2 THEN 'test' END)`, id, claimed); err != nil {
		t.Fatal(err)
	}
}

type cancelState struct {
	status    dom.Status
	requested bool
	by        string
	ended     bool
	items     int
}

func cancelStateOf(t *testing.T, pool *postgres.Pool, id uuid.UUID) cancelState {
	t.Helper()
	var s cancelState
	if err := pool.QueryRow(context.Background(), `
		SELECT status, cancel_requested_at IS NOT NULL, canceled_by, terminado_em IS NOT NULL,
		       (SELECT count(*) FROM queue_items WHERE run_id = $1)
		FROM runs WHERE id = $1`, id).Scan(&s.status, &s.requested, &s.by, &s.ended, &s.items); err != nil {
		t.Fatal(err)
	}
	return s
}

// A RUN STILL WAITING IS CANCELED AT ONCE, and its item leaves the queue in the
// same transaction: an item left behind would be claimed, refused `running`,
// and released straight back, forever.
func TestCancelingAWaitingRunCancelsItNow(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	for _, from := range []dom.Status{dom.StatusCreated, dom.StatusQueued, dom.StatusRetrying} {
		id := runAt(t, repo, from)
		if from != dom.StatusCreated {
			enqueued(t, pool, id, false)
		}

		got, err := repo.RequestCancel(ctx, id, "ana")
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		if got.Outcome != postgres.CancelDone {
			t.Errorf("%s: outcome %v, wanted CancelDone", from, got.Outcome)
		}
		want := cancelState{status: dom.StatusCanceled, requested: true, by: "ana", ended: true}
		if s := cancelStateOf(t, pool, id); s != want {
			t.Errorf("%s: %+v, wanted %+v", from, s, want)
		}
	}
}

// A run a dispatcher holds -- running, or claimed and about to be -- can only be
// asked: the dispatcher is the one that can stop its steps.
func TestCancelingAHeldRunAsksTheDispatcher(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	running := runAt(t, repo, dom.StatusRunning)
	enqueued(t, pool, running, true)
	claimed := runAt(t, repo, dom.StatusQueued)
	enqueued(t, pool, claimed, true)

	for id, status := range map[uuid.UUID]dom.Status{running: dom.StatusRunning, claimed: dom.StatusQueued} {
		got, err := repo.RequestCancel(ctx, id, "ana")
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != postgres.CancelRequested {
			t.Errorf("%s: outcome %v, wanted CancelRequested", status, got.Outcome)
		}
		want := cancelState{status: status, requested: true, by: "ana", items: 1}
		if s := cancelStateOf(t, pool, id); s != want {
			t.Errorf("%s: %+v, wanted %+v", status, s, want)
		}
	}
}

// Asking twice keeps who asked first: they are the one who stopped it.
func TestASecondCancelKeepsTheFirstActor(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	id := runAt(t, repo, dom.StatusRunning)
	enqueued(t, pool, id, true)
	for _, actor := range []string{"ana", "cli"} {
		if got, err := repo.RequestCancel(ctx, id, actor); err != nil || got.Outcome != postgres.CancelRequested {
			t.Fatalf("%s: %+v, %v", actor, got, err)
		}
	}
	if s := cancelStateOf(t, pool, id); s.by != "ana" {
		t.Errorf("canceled_by = %q, wanted the first, ana", s.by)
	}
}

// What has ended is left alone, and the answer says how it ended. A failed run
// is there too: a failure that will be retried is back in the queue at once,
// so a failed run is one that gave up.
func TestCancelingWhatHasEndedChangesNothing(t *testing.T) {
	pool, repo := runsDB(t)
	ctx := context.Background()
	for _, end := range []dom.Status{dom.StatusSuccess, dom.StatusFailed} {
		id := runAt(t, repo, end)
		before := cancelStateOf(t, pool, id)

		got, err := repo.RequestCancel(ctx, id, "ana")
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != postgres.CancelAlreadyEnded || got.Status != end {
			t.Errorf("%s: %+v, wanted CancelAlreadyEnded with %s", end, got, end)
		}
		if after := cancelStateOf(t, pool, id); after != before {
			t.Errorf("%s: %+v became %+v", end, before, after)
		}
	}

	id := runAt(t, repo, dom.StatusQueued)
	enqueued(t, pool, id, false)
	if _, err := repo.RequestCancel(ctx, id, "ana"); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.RequestCancel(ctx, id, "cli"); err != nil || got.Outcome != postgres.CancelAlreadyEnded ||
		got.Status != dom.StatusCanceled {
		t.Errorf("canceling a canceled run: %+v, %v", got, err)
	}
}

func TestCancelingAnUnknownRunIsAnError(t *testing.T) {
	_, repo := runsDB(t)
	if _, err := repo.RequestCancel(context.Background(), uuid.New(), "ana"); !errors.Is(err, postgres.ErrRunNotFound) {
		t.Errorf("err = %v, wanted ErrRunNotFound", err)
	}
}

// The feed says who canceled.
func TestTheFeedSaysWhoCanceled(t *testing.T) {
	pool := insightsDB(t)
	repo := postgres.NewRunRepo(pool)
	ctx := context.Background()
	id := runAt(t, repo, dom.StatusQueued)
	enqueued(t, pool, id, false)
	if _, err := repo.RequestCancel(ctx, id, "ana"); err != nil {
		t.Fatal(err)
	}

	got, _, err := postgres.NewEventRepo(pool).RecentEvents(ctx, postgres.EventQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Kind == events.RunCanceled && e.RunID != nil && *e.RunID == id {
			if e.Actor != "ana" {
				t.Errorf("actor = %q, wanted ana", e.Actor)
			}
			return
		}
	}
	t.Errorf("the canceled run is not in the feed: %s", kinds(got))
}

// beforeTheCancel is the last migration before the one that adds the cancel's
// columns.
const beforeTheCancel = 29

func columnExists(t *testing.T, pool *postgres.Pool, name string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'runs' AND column_name = $1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// Down removes the cancel's columns and up adds them again; the shared test
// database is left at the latest version whatever happens.
func TestTheCancelsMigrationGoesDownAndUp(t *testing.T) {
	pool := insightsDB(t)
	t.Cleanup(func() { migrate(t, "up") })

	if err := postgres.MigrateDownTo(context.Background(), os.Getenv("BREVIS_TEST_DATABASE_URL"), beforeTheCancel); err != nil {
		t.Fatalf("migrate down to %d: %v", beforeTheCancel, err)
	}
	for _, c := range []string{"cancel_requested_at", "canceled_by"} {
		if columnExists(t, pool, c) {
			t.Errorf("after down runs.%s is still there", c)
		}
	}
	migrate(t, "up")
	for _, c := range []string{"cancel_requested_at", "canceled_by"} {
		if !columnExists(t, pool, c) {
			t.Errorf("after up runs.%s is missing", c)
		}
	}
}
