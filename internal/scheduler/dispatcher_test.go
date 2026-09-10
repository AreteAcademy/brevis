package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wfdom "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// These tests require Postgres. Without BREVIS_TEST_DATABASE_URL they skip, so
// `go test ./...` stays green on a machine with no docker.
func testDB(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the queue tests (make up)")
	}
	p, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)

	// Every test starts from scratch. `schedules` is on the list even with no FK
	// to workflows: it references the slug as text, so the CASCADE does not
	// reach it.
	if _, err := p.Exec(context.Background(),
		`TRUNCATE queue_items, task_runs, runs, schedules, workflows, projects CASCADE`); err != nil {
		t.Fatal(err)
	}
	return p
}

func noLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// CRITERIO DE ACEITE DA PHASE 2 (secao 37):
//
//	100 runs enfileiradas, concorrencia maxima 5
//	-> 5 RUNNING, 95 QUEUED, nothing lost.
func TestAcceptanceCriterion_100Runs_Concurrency5(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	const total, maxConc = 100, 5

	for i := 0; i < total; i++ {
		r, err := repo.Create(ctx, dom.Run{
			WorkflowSlug:   "teste",
			IdempotencyKey: fmt.Sprintf("aceite-%d", i),
			Definition:     []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	// It holds every run until we release, so the steady state is observable.
	segurar := make(chan struct{})
	var running atomic.Int32
	var pico atomic.Int32

	executar := func(ctx context.Context, _ uuid.UUID) error {
		n := running.Add(1)
		for {
			p := pico.Load()
			if n <= p || pico.CompareAndSwap(p, n) {
				break
			}
		}
		defer running.Add(-1)
		<-segurar
		return nil
	}

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: maxConc, Interval: 20 * time.Millisecond,
	}, queue, repo, executar, noLog())

	ctxD, parar := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = d.Run(ctxD) }()

	// Espera o estado estabilizar em maxConc em voo.
	prazo := time.After(10 * time.Second)
	for running.Load() < maxConc {
		select {
		case <-prazo:
			t.Fatalf("only %d in flight after 10s, wanted %d", running.Load(), maxConc)
		case <-time.After(20 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond) // deixa o dispatcher tentar pegar mais

	count, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending, claimed, err := queue.Size(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("runs: %v | queue: %d pending, %d claimed | in flight: %d",
		count, pending, claimed, running.Load())

	if got := count[dom.StatusRunning]; got != maxConc {
		t.Errorf("RUNNING = %d, wanted %d", got, maxConc)
	}
	if got := count[dom.StatusQueued]; got != total-maxConc {
		t.Errorf("QUEUED = %d, wanted %d", got, total-maxConc)
	}
	if p := pico.Load(); p > maxConc {
		t.Errorf("pico de concorrencia = %d, excedeu o maximo de %d", p, maxConc)
	}
	if pending+claimed != total {
		t.Errorf("the queue holds %d items, wanted %d -- something was lost", pending+claimed, total)
	}

	// Release, and confirm all 100 finish with nothing lost.
	close(segurar)
	prazo = time.After(30 * time.Second)
	for {
		c, err := repo.CountByStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c[dom.StatusSuccess] == total {
			break
		}
		select {
		case <-prazo:
			t.Fatalf("after releasing: %v -- wanted %d at success", c, total)
		case <-time.After(50 * time.Millisecond):
		}
	}
	parar()
	wg.Wait()

	pending, claimed, _ = queue.Size(ctx)
	if pending+claimed != 0 {
		t.Errorf("the queue should be empty, it holds %d pending and %d claimed", pending, claimed)
	}
	if p := pico.Load(); p > maxConc {
		t.Errorf("pico de concorrencia = %d durante toda a corrida", p)
	}
}

// A critical operation has to tolerate repetition. The concrete case: the
// scheduler creates the Run, dies before recording it, and tries again on the
// way back up.
func TestIdempotencyPreventsADuplicateRun(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	r := dom.Run{WorkflowSlug: "w", IdempotencyKey: "mesma-chave", Definition: []byte(`{}`)}
	if _, err := repo.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "mesma-chave", Definition: []byte(`{}`),
	})
	if !errors.Is(err, postgres.ErrJaExiste) {
		t.Fatalf("error = %v, wanted ErrJaExiste", err)
	}
}

// Queuing the same run twice is a no-op, not a duplicate.
func TestEnqueueIsIdempotent(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "k", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	pending, _, err := queue.Size(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Errorf("the queue holds %d items, wanted 1", pending)
	}
}

// Two competing dispatchers must not receive the same item -- which is what
// FOR UPDATE SKIP LOCKED garante.
func TestClaimDoesNotHandOutTheSameItemTwice(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	const n = 20
	for i := 0; i < n; i++ {
		r, err := repo.Create(ctx, dom.Run{
			WorkflowSlug: "w", IdempotencyKey: fmt.Sprintf("c-%d", i), Definition: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	vistos := map[uuid.UUID]int{}
	var wg sync.WaitGroup

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			items, err := queue.Claim(ctx, fmt.Sprintf("worker-%d", w), n)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, it := range items {
				vistos[it.RunID]++
			}
			mu.Unlock()
		}(w)
	}
	wg.Wait()

	if len(vistos) != n {
		t.Errorf("%d runs claimed, wanted %d", len(vistos), n)
	}
	for id, c := range vistos {
		if c > 1 {
			t.Errorf("run %s entregue %d vezes", id, c)
		}
	}
}

// An item stuck to a dead worker has to come back. Without this, it is the
// zombie run that stalled pipelines for 33 days in the previous system.
func TestRecoverReturnsADeadWorkersItem(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "z", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
		t.Fatal(err)
	}

	if _, claimed, _ := queue.Size(ctx); claimed != 1 {
		t.Fatal("esperava 1 item reivindicado")
	}
	items, err := queue.Recover(ctx, 0) // a zero limit: everything claimed comes back
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("recovered %d, wanted 1", len(items))
	}
	if len(items) == 1 && items[0].RunID != r.ID {
		t.Errorf("the recovered item points at %s, wanted %s", items[0].RunID, r.ID)
	}
	if pending, _, _ := queue.Size(ctx); pending != 1 {
		t.Error("o item deveria estar livre de novo")
	}
}

// The bug the user saw on screen: the worker dies partway, the item goes back to
// the queue but the RUN stays "running" forever. The sweep has to fix both
// sides.
func TestRecoveringOrphansPutsTheRunBackInTheQueue(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "orfa", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
		t.Fatal(err)
	}
	// The worker did get as far as marking running before dying -- the exact
	// state in which
	// a run ficava pendurada.
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}

	d := scheduler.New(scheduler.Config{
		Worker: "vivo", MaxAttempts: 3, Visibility: time.Nanosecond,
	}, queue, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())

	n, err := d.RecoverOrphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered %d orphans, wanted 1", n)
	}

	after, err := repo.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != dom.StatusQueued {
		t.Errorf("the run stayed at %s; wanted queued, ready for another worker", after.Status)
	}
	if after.Attempt != 1 {
		t.Errorf("attempt = %d; the worker dying spends an attempt", after.Attempt)
	}
	if after.Err == "" {
		t.Error("the run has to record WHY it was recovered")
	}
	if pending, _, _ := queue.Size(ctx); pending != 1 {
		t.Error("the item should be free for another worker")
	}
}

// With the attempts exhausted, the orphan stops at failed instead of circling
// between workers forever.
func TestAnOrphanStopsComingBackWhenTheAttemptsRunOut(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "orfa2", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	d := scheduler.New(scheduler.Config{
		Worker: "vivo", MaxAttempts: 1, Visibility: time.Nanosecond,
	}, queue, repo, func(context.Context, uuid.UUID) error { return nil }, noLog())

	if _, err := queue.Claim(ctx, "morto", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecoverOrphans(ctx); err != nil {
		t.Fatal(err)
	}

	after, _ := repo.Get(ctx, r.ID)
	if after.Status != dom.StatusFailed {
		t.Errorf("the run is at %s; with the attempts spent it has to stop at failed", after.Status)
	}
	if pending, claimed, _ := queue.Size(ctx); pending+claimed != 0 {
		t.Errorf("the queue holds %d items; the spent orphan has to leave it", pending+claimed)
	}
}

// raised reads what the dispatcher wrote into the outbox.
//
// The dispatcher no longer TALKS to anything -- there is no fake channel here
// any more, because there is no call to fake. It writes a row in the same
// transaction as the failure, and these tests read that row.
func raised(t *testing.T, pool *postgres.Pool, runID uuid.UUID) []alerts.Record {
	t.Helper()
	out, err := alerts.New(pool.Pool).ForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The alert fires ONCE, when the run gives up -- not on every attempt.
// Announcing every failure would turn a successful retry into two alerts and a
// silence, and a channel that shouts for nothing stops being read.
func TestTheAlertGoesOutOnceWhenTheAttemptsRunOut(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "falha",
		TriggerType: "schedule", Definition: []byte(`{"Tags":["acme","id","dbt"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 3,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, queue, repo, func(context.Context, uuid.UUID) error {
		return errors.New(`step "run": exited with code 2`)
	}, noLog())
	d.Channel = alerts.ChannelSlack
	d.BaseURL = "https://brevis.example.com"

	go func() { _ = d.Run(ctx) }()

	// Espera o run esgotar as tentativas.
	prazo := time.Now().Add(15 * time.Second)
	for time.Now().Before(prazo) {
		current, _ := repo.Get(ctx, r.ID)
		if current.Status == dom.StatusFailed && current.Attempt >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)

	rows := raised(t, pool, r.ID)
	if len(rows) != 1 {
		t.Fatalf("%d alerts for a run that failed once; want exactly 1", len(rows))
	}
	if rows[0].Kind != alerts.KindRun || rows[0].Channel != alerts.ChannelSlack {
		t.Errorf("the row does not say what raised it or where it goes: %+v", rows[0])
	}
	// It is written, not delivered. Delivery is the alert pod's job, and the
	// row saying so is what lets the screen tell "nobody was told yet" from
	// "nobody will be told".
	if rows[0].State() != "sending" {
		t.Errorf("a freshly raised alert reads as %q", rows[0].State())
	}

	a := rows[0].Payload
	if a.Workflow != "id_verification" || a.Trigger != "schedule" {
		t.Errorf("the alert has none of the run's details: %+v", a)
	}
	if len(a.Tags) != 3 || a.Tags[1] != "id" {
		t.Errorf("the snapshot's tags did not arrive: %v", a.Tags)
	}
	if !strings.Contains(a.Err, "code 2") {
		t.Errorf("the alert has no cause: %q", a.Err)
	}
	if a.BaseURL == "" || a.RunID != r.ID.String() {
		t.Errorf("the alert has no link to the run: %+v", a)
	}
}

// The attempt and the alert are ONE write, or neither.
//
// This is the outbox's whole claim and the reason the alert is not sent from
// here. Before it, the dispatcher spent the attempt and then called Slack: a
// process dying between the two left a run out of attempts with nobody told
// and no record that anybody should have been.
//
// The alert is made unwritable by pointing its row at a run that does not
// exist, which the foreign key refuses. What has to survive that is the
// ATTEMPT: it must still read what it read before.
func TestAnAlertThatCannotBeWrittenRollsTheAttemptBack(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "atomic", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	before, err := repo.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = repo.Attempt(ctx, r.ID, 1, func(int, bool) []alerts.Pending {
		return []alerts.Pending{{
			RunID:   uuid.New(), // no such run: the foreign key refuses it
			Kind:    alerts.KindRun,
			Channel: alerts.ChannelSlack,
		}}
	})
	if err == nil {
		t.Fatal("writing an alert for a run that does not exist should have failed")
	}

	after, err := repo.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Attempt != before.Attempt {
		t.Errorf("the attempt went from %d to %d while the alert was lost: "+
			"the two are supposed to commit together", before.Attempt, after.Attempt)
	}
}

// With no channel configured, nothing is written at all.
//
// An outbox filling with alerts nothing can deliver would read as a backlog
// instead of as a setting nobody turned on, and every one of those rows would
// end up marked undelivered -- which looks exactly like Slack rejecting them.
func TestWithNoChannelNoAlertIsWritten(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, _ := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "silent", Definition: []byte(`{}`),
	})
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = q.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 1,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, q, repo, func(context.Context, uuid.UUID) error {
		return errors.New("failed")
	}, noLog())
	// d.Channel deliberately left empty.

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool {
		current, _ := repo.Get(context.Background(), r.ID)
		return current.Status == dom.StatusFailed && current.Attempt >= 1
	})
	cancel()
	time.Sleep(100 * time.Millisecond)

	if rows := raised(t, pool, r.ID); len(rows) != 0 {
		t.Errorf("%d alert(s) written with no channel configured", len(rows))
	}
	if pending, claimed, _ := q.Size(context.Background()); pending+claimed != 0 {
		t.Errorf("the item got stuck in the queue")
	}
}

func enqueue(t *testing.T, repo *postgres.RunRepo, queue *queue.Queue,
	slug string, quantos, maxActive int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < quantos; i++ {
		r, err := repo.Create(ctx, dom.Run{
			WorkflowSlug: slug, IdempotencyKey: fmt.Sprintf("%s-%d", slug, i),
			Definition: []byte(`{}`), MaxActive: maxActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

// The case that motivated all of it: a `*/15` that takes 20 minutes overlaps
// itself, and two `dbt build`s on the SAME model fight over the same table.
//
// Five items of the same workflow with a limit of 1: the claim hands out ONE,
// however many global slots there are.
func TestThePerWorkflowLimitHoldsTheRestBack(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	enqueue(t, repo, queue, "id_verification_today", 5, 1)

	items, err := queue.Claim(ctx, "w", 10) // dez vagas globais
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("claim handed out %d items; the workflow limit is 1", len(items))
	}

	// Until the first finishes, nobody else goes in.
	outros, err := queue.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outros) != 0 {
		t.Errorf("it handed out %d more with the first still in flight", len(outros))
	}

	// Terminado o primeiro, o proximo entra.
	if err := queue.Done(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	seguintes, err := queue.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(seguintes) != 1 {
		t.Errorf("after the slot was released, claim handed out %d; wanted 1", len(seguintes))
	}
}

// A limit greater than 1 hands out exactly the limit -- no fewer (which would
// be serialisation), and no more.
func TestALimitOfThreeHandsOutThree(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	enqueue(t, repo, queue, "vendors_x", 8, 3)

	items, err := queue.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Errorf("claim handed out %d; the limit is 3", len(items))
	}
}

// A workflow at its limit must not block the others: the queue is shared, and
// stalling everything because of one would be worse than having no limit.
func TestAWorkflowAtItsLimitDoesNotBlockTheOthers(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	enqueue(t, repo, queue, "travado", 5, 1)
	enqueue(t, repo, queue, "livre_a", 2, 0)
	enqueue(t, repo, queue, "livre_b", 2, 0)

	items, err := queue.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	// 1 from the blocked one + 4 from the free ones.
	if len(items) != 5 {
		t.Errorf("claim handed out %d; wanted 5 (1 limited + 4 unlimited)", len(items))
	}
}

// With no limit declared (0) nothing changes -- the old behaviour stays the
// default.
func TestWithNoLimitItHandsOutEverythingThatFits(t *testing.T) {
	pool := testDB(t)
	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	enqueue(t, repo, queue, "sem_limite", 6, 0)

	items, err := queue.Claim(context.Background(), "w", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Errorf("claim handed out %d; the global allowance was 4", len(items))
	}
}

// A run that fails and passes on the second attempt does NOT alert.
//
// It is the rule's other half: the alert exists for a definitive failure.
// Announcing a failure the retry itself fixed trains the team to ignore the
// channel, and then the alert that matters goes past unnoticed with it.
func TestASucceedingRetryDoesNotAlert(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "retry-ok",
		TriggerType: "schedule", Definition: []byte(`{"Tags":["acme","id"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	var calls int32
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 3,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, queue, repo, func(context.Context, uuid.UUID) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New(`step "run": exited with code 2`)
		}
		return nil // the second attempt passes
	}, noLog())
	d.Channel = alerts.ChannelSlack

	go func() { _ = d.Run(ctx) }()

	prazo := time.Now().Add(15 * time.Second)
	for time.Now().Before(prazo) {
		if current, _ := repo.Get(ctx, r.ID); current.Status == dom.StatusSuccess {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	time.Sleep(150 * time.Millisecond)

	if current, _ := repo.Get(context.Background(), r.ID); current.Status != dom.StatusSuccess {
		t.Fatalf("the run finished as %s; the test needs it to pass on the second try", current.Status)
	}
	if rows := raised(t, pool, r.ID); len(rows) != 0 {
		t.Errorf("%d alert(s) raised for a run that recovered on its own", len(rows))
	}
}

// The alert has to name the step and carry the end of that step's log.
//
// Without this it only says something failed, and whoever is on call at 4am
// opens the screen to find out what -- which is exactly the work the alert was
// supposed to
// poupar.
func TestTheAlertCarriesTheStepAndTheLog(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "vendors_inmet_observation", IdempotencyKey: "com-log",
		TriggerType: "schedule", Definition: []byte(`{"Tags":["acme","vendors"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 1,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, queue, repo, func(ctx context.Context, id uuid.UUID) error {
		// Writes the task the way the runner would, with output.
		if err := repo.IniciarTask(ctx, id, dom.Step("fetch_observations"), 0); err != nil {
			return err
		}
		output := "conectando na api do inmet\nHTTP 503 Service Unavailable\ndesistindo after 3 attempts"
		codigo := 1
		if err := repo.TerminarTask(ctx, id, dom.Step("fetch_observations"), 0,
			dom.StatusFailed, &codigo, "exited with code 1", output); err != nil {
			return err
		}
		return errors.New(`step "fetch_observations": exited with code 1`)
	}, noLog())
	d.Channel = alerts.ChannelSlack

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool { return len(raised(t, pool, r.ID)) > 0 })
	cancel()
	time.Sleep(100 * time.Millisecond)

	rows := raised(t, pool, r.ID)
	if len(rows) == 0 {
		t.Fatal("no alert was raised")
	}
	a := rows[0].Payload
	if a.Step != "fetch_observations" {
		t.Errorf("Step = %q; expected the node that failed", a.Step)
	}
	if !strings.Contains(a.LogExcerpt, "503 Service Unavailable") {
		t.Errorf("LogExcerpt does not carry the cause: %q", a.LogExcerpt)
	}
}

// A SIGTERM landing as a run finishes must not strand its queue item.
//
// `Run` waits for in-flight work before returning, and that promise was empty:
// everything after `executar` used the RUN's context, which the signal had
// already cancelled, so `Transicionar` and `Done` failed in silence. The run
// came out marked SUCCESS with its item still claimed, and the visibility sweep
// returned it fifteen minutes later and counted it as a FAILED attempt of a run
// that had succeeded.
//
// The cancellation is fired from inside the executor rather than raced from
// outside, so the window is not "usually" hit -- it is always hit.
func TestAShutdownWhileARunFinishesDoesNotStrandTheItem(t *testing.T) {
	pool := testDB(t)
	base := context.Background()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(base, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "shutdown-mid-finish",
		TriggerType: "manual", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(base, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(base, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(base)
	defer stop()
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, Interval: 10 * time.Millisecond,
	}, q, repo, func(context.Context, uuid.UUID) error {
		stop() // the signal arrives while the run is on its way out
		return nil
	}, noLog())

	finished := make(chan error, 1)
	go func() { finished <- d.Run(ctx) }()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("Run never returned")
	}

	current, err := repo.Get(base, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != dom.StatusSuccess {
		t.Errorf("the run came out as %s; the shutdown ate the transition", current.Status)
	}
	pending, claimed, err := q.Size(base)
	if err != nil {
		t.Fatal(err)
	}
	if pending+claimed != 0 {
		t.Errorf("the queue holds %d pending and %d claimed: the item was stranded, "+
			"and only the visibility sweep will free it", pending, claimed)
	}
}

// definitionWith builds the run's snapshot the way `publish` would.
func definitionWith(t *testing.T, nodes ...wfdom.Node) []byte {
	t.Helper()
	raw, err := json.Marshal(wfdom.Workflow{Slug: "id_verification", Nodes: nodes})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func slackOnGiveUp() *wfdom.OnError {
	return &wfdom.OnError{Type: wfdom.ChannelSlack}
}

// A step that declared on_error and failed gets an alert of its own, beside the
// run's.
//
// Two messages and not one: the run-level alert says "id_verification failed",
// which is what whoever owns the pipeline needs, and the step-level one says
// "fetch_observations failed", which is what whoever owns that integration
// needs. Collapsing them would mean the second person reads the first person's
// alert and has to work out whether it concerns them.
func TestAStepThatDeclaredOnErrorGetsItsOwnAlert(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "step-alert",
		TriggerType: "schedule",
		Definition: definitionWith(t,
			wfdom.Node{ID: "fetch", Run: "python fetch.py", OnError: slackOnGiveUp()},
			// Declares an alert and never fails: a workflow where one branch
			// breaks must not wake up whoever owns the other one.
			wfdom.Node{ID: "report", Run: "python report.py", OnError: slackOnGiveUp()},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = q.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 1,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, q, repo, func(ctx context.Context, id uuid.UUID) error {
		if err := repo.IniciarTask(ctx, id, dom.Step("fetch"), 0); err != nil {
			return err
		}
		code := 1
		if err := repo.TerminarTask(ctx, id, dom.Step("fetch"), 0, dom.StatusFailed, &code,
			"exited with code 1", "HTTP 503 from the vendor"); err != nil {
			return err
		}
		return errors.New(`step "fetch": exited with code 1`)
	}, noLog())
	d.Channel = wfdom.ChannelSlack

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool { return len(raised(t, pool, r.ID)) >= 2 })
	cancel()
	time.Sleep(100 * time.Millisecond)

	rows := raised(t, pool, r.ID)
	var runLevel, stepLevel []alerts.Record
	for _, a := range rows {
		if a.Kind == alerts.KindStep {
			stepLevel = append(stepLevel, a)
		} else {
			runLevel = append(runLevel, a)
		}
	}
	if len(runLevel) != 1 {
		t.Errorf("%d run-level alerts, wanted 1", len(runLevel))
	}
	if len(stepLevel) != 1 {
		t.Fatalf("%d step-level alerts, wanted 1 (only `fetch` failed)", len(stepLevel))
	}
	if stepLevel[0].NodeID != "fetch" {
		t.Errorf("the step alert names %q", stepLevel[0].NodeID)
	}
	if !strings.Contains(stepLevel[0].Payload.LogExcerpt, "503") {
		t.Errorf("the step alert carries no evidence: %q", stepLevel[0].Payload.LogExcerpt)
	}
}

// `when: attempt` announces a failure that is going to be retried; the default
// does not.
//
// The default is the quiet one because of whose night it is: a step that fails
// twice and passes on the third try would send two messages under the other
// default, and the second one would arrive after the problem was gone.
func TestWhenAttemptAnnouncesAFailureThatWillBeRetried(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "when-attempt",
		TriggerType: "schedule",
		Definition: definitionWith(t, wfdom.Node{
			ID: "fetch", Run: "python fetch.py",
			OnError: &wfdom.OnError{Type: wfdom.ChannelSlack, When: wfdom.OnAttempt},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = q.Enqueue(ctx, r.ID, 0, time.Time{})

	var calls int32
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 3,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, q, repo, func(ctx context.Context, id uuid.UUID) error {
		n := int(atomic.AddInt32(&calls, 1))
		if n > 1 {
			return nil // the second attempt passes
		}
		_ = repo.IniciarTask(ctx, id, dom.Step("fetch"), 0)
		code := 1
		_ = repo.TerminarTask(ctx, id, dom.Step("fetch"), 0, dom.StatusFailed, &code, "boom", "boom")
		return errors.New(`step "fetch": exited with code 1`)
	}, noLog())
	d.Channel = wfdom.ChannelSlack

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool {
		current, _ := repo.Get(context.Background(), r.ID)
		return current.Status == dom.StatusSuccess
	})
	cancel()
	time.Sleep(150 * time.Millisecond)

	rows := raised(t, pool, r.ID)
	if len(rows) != 1 {
		t.Fatalf("%d alerts; `when: attempt` should have raised exactly the one failed attempt", len(rows))
	}
	if rows[0].Kind != alerts.KindStep || rows[0].NodeID != "fetch" {
		t.Errorf("the alert is not the step's: %+v", rows[0].Item)
	}
	// And no run-level alert: the run recovered, so nobody owns a failure.
	for _, a := range rows {
		if a.Kind == alerts.KindRun {
			t.Error("a run that recovered raised a run-level alert")
		}
	}
}

// A step naming a channel the installation has not configured is DROPPED, not
// written. The outbox is for alerts that could have arrived; a row that was
// never deliverable would come out marked undelivered and look exactly like
// Slack rejecting it.
func TestAStepNamingAnUnconfiguredChannelIsNotWritten(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "wrong-channel", TriggerType: "manual",
		// A definition from a build that knew a channel this one does not.
		Definition: definitionWith(t, wfdom.Node{
			ID: "fetch", Run: "x", OnError: &wfdom.OnError{Type: "TEAMS"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = q.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 1,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, q, repo, func(ctx context.Context, id uuid.UUID) error {
		_ = repo.IniciarTask(ctx, id, dom.Step("fetch"), 0)
		code := 1
		_ = repo.TerminarTask(ctx, id, dom.Step("fetch"), 0, dom.StatusFailed, &code, "boom", "boom")
		return errors.New("boom")
	}, noLog())
	d.Channel = wfdom.ChannelSlack

	go func() { _ = d.Run(ctx) }()
	waitFor(t, func() bool { return len(raised(t, pool, r.ID)) > 0 })
	cancel()
	time.Sleep(100 * time.Millisecond)

	for _, a := range raised(t, pool, r.ID) {
		if a.Kind == alerts.KindStep {
			t.Errorf("an alert was written for a channel nothing delivers to: %+v", a.Item)
		}
	}
}

// TestTheBackoffActuallyDelaysTheNextAttempt.
//
// backoff() is unit-tested and the flags are tested where they are bound. What
// neither covers is the line between them: process() has to hand the delay to
// queue.Release, and if it handed zero every one of those tests would still
// pass while every retry fired instantly.
//
// That is the shape this repository keeps hitting -- a value computed
// correctly and passed nowhere -- so it is measured against a real queue: the
// gap between two attempts of the same run.
func TestTheBackoffActuallyDelaysTheNextAttempt(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	q := queue.New(pool.Pool)

	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "backoff", IdempotencyKey: "delayed",
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

	// Long enough to measure against a 10ms poll, short enough that the test
	// is not a nap.
	const base = 400 * time.Millisecond

	var mu sync.Mutex
	var starts []time.Time
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 2,
		Interval: 10 * time.Millisecond, BackoffBase: base, BackoffMax: time.Hour,
	}, q, repo, func(context.Context, uuid.UUID) error {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return errors.New(`step "fetch": exited with code 2`)
	}, noLog())

	go func() { _ = d.Run(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		enough := len(starts) >= 2
		mu.Unlock()
		if enough {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(starts) < 2 {
		t.Fatalf("the run was attempted %d time(s); it has to be retried", len(starts))
	}
	gap := starts[1].Sub(starts[0])
	if gap < base {
		t.Errorf("the second attempt started %s after the first, and the backoff "+
			"is %s -- the delay is computed and not applied", gap, base)
	}
	// And not absurdly more, which would mean it is waiting on the poll rather
	// than on the backoff and the assertion above proves nothing.
	if gap > base+5*time.Second {
		t.Errorf("the second attempt started %s after the first, far beyond the "+
			"%s backoff", gap, base)
	}
}
