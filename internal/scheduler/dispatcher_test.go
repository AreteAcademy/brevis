package scheduler_test

import (
	"context"
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

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/notify"
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

// Section 29 asks that a critical operation tolerate repetition. The concrete
// case: the
// scheduler cria o Run, morre antes de registrar e tenta de novo ao subir.
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

type alertaFalso struct {
	mu       sync.Mutex
	received []notify.Alert
	failure  error
}

func (a *alertaFalso) Failed(_ context.Context, al notify.Alert) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.received = append(a.received, al)
	return a.failure
}

func (a *alertaFalso) total() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.received)
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

	avisos := &alertaFalso{}
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 3,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, queue, repo, func(context.Context, uuid.UUID) error {
		return errors.New(`step "run": exited with code 2`)
	}, noLog())
	d.Alerts = avisos
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

	if n := avisos.total(); n != 1 {
		t.Fatalf("%d alerts for a run that failed once; want exactly 1", n)
	}

	a := avisos.received[0]
	if a.Workflow != "id_verification" || a.Trigger != "schedule" {
		t.Errorf("the alert has none of the run's details: %+v", a)
	}
	if len(a.Tags) != 3 || a.Tags[1] != "id" {
		t.Errorf("the snapshot's tags did not arrive: %v", a.Tags)
	}
	if !strings.Contains(a.Err, "codigo 2") {
		t.Errorf("the alert has no cause: %q", a.Err)
	}
	if a.BaseURL == "" || a.RunID != r.ID.String() {
		t.Errorf("the alert has no link to the run: %+v", a)
	}
}

// A webhook that is down must not stop the dispatcher: the run has to end FAILED
// and the queue has to keep being consumed.
func TestAFailureToNotifyDoesNotTakeTheDispatcherDown(t *testing.T) {
	pool := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	queue := queue.New(pool.Pool)

	r, _ := repo.Create(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "x", Definition: []byte(`{}`),
	})
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = queue.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 1,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, queue, repo, func(context.Context, uuid.UUID) error {
		return errors.New("falhou")
	}, noLog())
	d.Alerts = &alertaFalso{failure: errors.New("slack respondeu 500")}

	go func() { _ = d.Run(ctx) }()

	prazo := time.Now().Add(10 * time.Second)
	for time.Now().Before(prazo) {
		current, _ := repo.Get(ctx, r.ID)
		if current.Status == dom.StatusFailed {
			cancel()
			if pending, claimed, _ := queue.Size(ctx); pending+claimed != 0 {
				t.Errorf("the item got stuck in the queue after the alert failed")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run never finished: the alert's error hung the dispatcher")
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
// be
// serializacao), nem mais.
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

// Sem limite declarado (0), nada muda — o comportamento antigo continua sendo o
// padrao.
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

	avisos := &alertaFalso{}
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
	d.Alerts = avisos

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
	if n := avisos.total(); n != 0 {
		t.Errorf("%d alert(s) went out for a run that recovered on its own", n)
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

	avisos := &alertaFalso{}
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxAttempts: 1,
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, queue, repo, func(ctx context.Context, id uuid.UUID) error {
		// Writes the task the way the runner would, with output.
		if err := repo.IniciarTask(ctx, id, "fetch_observations", 0); err != nil {
			return err
		}
		output := "conectando na api do inmet\nHTTP 503 Service Unavailable\ndesistindo after 3 attempts"
		codigo := 1
		if err := repo.TerminarTask(ctx, id, "fetch_observations", 0,
			dom.StatusFailed, &codigo, "exited with code 1", output); err != nil {
			return err
		}
		return errors.New(`step "fetch_observations": exited with code 1`)
	}, noLog())
	d.Alerts = avisos

	go func() { _ = d.Run(ctx) }()

	prazo := time.Now().Add(15 * time.Second)
	for time.Now().Before(prazo) {
		if avisos.total() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)

	avisos.mu.Lock()
	defer avisos.mu.Unlock()
	if len(avisos.received) == 0 {
		t.Fatal("nenhum alert saiu")
	}
	a := avisos.received[0]
	if a.Step != "fetch_observations" {
		t.Errorf("Step = %q; expected the node that failed", a.Step)
	}
	if !strings.Contains(a.LogExcerpt, "503 Service Unavailable") {
		t.Errorf("LogExcerpt does not carry the cause: %q", a.LogExcerpt)
	}
}
