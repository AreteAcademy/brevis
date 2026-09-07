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
func banco(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("defina BREVIS_TEST_DATABASE_URL para rodar os testes de fila (make up)")
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

func semLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// CRITERIO DE ACEITE DA PHASE 2 (secao 37):
//
//	100 runs enfileiradas, concorrencia maxima 5
//	-> 5 RUNNING, 95 QUEUED, nothing lost.
func TestCriterioDeAceite_100Runs_Concorrencia5(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	const total, maxConc = 100, 5

	for i := 0; i < total; i++ {
		r, err := repo.Criar(ctx, dom.Run{
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
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	// It holds every run until we release, so the steady state is observable.
	segurar := make(chan struct{})
	var rodando atomic.Int32
	var pico atomic.Int32

	executar := func(ctx context.Context, _ uuid.UUID) error {
		n := rodando.Add(1)
		for {
			p := pico.Load()
			if n <= p || pico.CompareAndSwap(p, n) {
				break
			}
		}
		defer rodando.Add(-1)
		<-segurar
		return nil
	}

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: maxConc, Intervalo: 20 * time.Millisecond,
	}, fila, repo, executar, semLog())

	ctxD, parar := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = d.Run(ctxD) }()

	// Espera o estado estabilizar em maxConc em voo.
	prazo := time.After(10 * time.Second)
	for rodando.Load() < maxConc {
		select {
		case <-prazo:
			t.Fatalf("so %d em voo depois de 10s, queria %d", rodando.Load(), maxConc)
		case <-time.After(20 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond) // deixa o dispatcher tentar pegar mais

	contagem, err := repo.ContarPorStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pendentes, reivindicados, err := fila.Tamanho(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("runs: %v | fila: %d pendentes, %d reivindicados | em voo: %d",
		contagem, pendentes, reivindicados, rodando.Load())

	if got := contagem[dom.StatusRunning]; got != maxConc {
		t.Errorf("RUNNING = %d, queria %d", got, maxConc)
	}
	if got := contagem[dom.StatusQueued]; got != total-maxConc {
		t.Errorf("QUEUED = %d, queria %d", got, total-maxConc)
	}
	if p := pico.Load(); p > maxConc {
		t.Errorf("pico de concorrencia = %d, excedeu o maximo de %d", p, maxConc)
	}
	if pendentes+reivindicados != total {
		t.Errorf("fila tem %d itens, queria %d — houve perda", pendentes+reivindicados, total)
	}

	// Release, and confirm all 100 finish with nothing lost.
	close(segurar)
	prazo = time.After(30 * time.Second)
	for {
		c, err := repo.ContarPorStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c[dom.StatusSuccess] == total {
			break
		}
		select {
		case <-prazo:
			t.Fatalf("apos liberar: %v — queria %d em success", c, total)
		case <-time.After(50 * time.Millisecond):
		}
	}
	parar()
	wg.Wait()

	pendentes, reivindicados, _ = fila.Tamanho(ctx)
	if pendentes+reivindicados != 0 {
		t.Errorf("fila deveria estar vazia, tem %d pendentes e %d reivindicados", pendentes, reivindicados)
	}
	if p := pico.Load(); p > maxConc {
		t.Errorf("pico de concorrencia = %d durante toda a corrida", p)
	}
}

// Section 29 asks that a critical operation tolerate repetition. The concrete
// case: the
// scheduler cria o Run, morre antes de registrar e tenta de novo ao subir.
func TestIdempotenciaImpedeRunDuplicado(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	r := dom.Run{WorkflowSlug: "w", IdempotencyKey: "mesma-chave", Definition: []byte(`{}`)}
	if _, err := repo.Criar(ctx, r); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "mesma-chave", Definition: []byte(`{}`),
	})
	if !errors.Is(err, postgres.ErrJaExiste) {
		t.Fatalf("erro = %v, queria ErrJaExiste", err)
	}
}

// Queuing the same run twice is a no-op, not a duplicate.
func TestEnqueueEhIdempotente(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "k", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	pendentes, _, err := fila.Tamanho(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pendentes != 1 {
		t.Errorf("fila tem %d itens, queria 1", pendentes)
	}
}

// Two competing dispatchers must not receive the same item -- which is what
// FOR UPDATE SKIP LOCKED garante.
func TestClaimNaoEntregaOMesmoItemDuasVezes(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	const n = 20
	for i := 0; i < n; i++ {
		r, err := repo.Criar(ctx, dom.Run{
			WorkflowSlug: "w", IdempotencyKey: fmt.Sprintf("c-%d", i), Definition: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
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
			itens, err := fila.Claim(ctx, fmt.Sprintf("worker-%d", w), n)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, it := range itens {
				vistos[it.RunID]++
			}
			mu.Unlock()
		}(w)
	}
	wg.Wait()

	if len(vistos) != n {
		t.Errorf("%d runs reivindicados, queria %d", len(vistos), n)
	}
	for id, c := range vistos {
		if c > 1 {
			t.Errorf("run %s entregue %d vezes", id, c)
		}
	}
}

// An item stuck to a dead worker has to come back. Without this, it is the
// zombie run that stalled pipelines for 33 days in the previous system.
func TestRecuperarDevolveItemDeWorkerMorto(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "z", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fila.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
		t.Fatal(err)
	}

	if _, reivindicados, _ := fila.Tamanho(ctx); reivindicados != 1 {
		t.Fatal("esperava 1 item reivindicado")
	}
	itens, err := fila.Recuperar(ctx, 0) // a zero limit: everything claimed comes back
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 1 {
		t.Errorf("recuperou %d, queria 1", len(itens))
	}
	if len(itens) == 1 && itens[0].RunID != r.ID {
		t.Errorf("item recuperado aponta para %s, queria %s", itens[0].RunID, r.ID)
	}
	if pendentes, _, _ := fila.Tamanho(ctx); pendentes != 1 {
		t.Error("o item deveria estar livre de novo")
	}
}

// The bug the user saw on screen: the worker dies partway, the item goes back to
// the queue but the RUN stays "running" forever. The sweep has to fix both
// sides.
func TestRecuperarOrfaosDevolveORunAFila(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "orfa", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fila.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
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
		Worker: "vivo", MaxTentativas: 3, Visibilidade: time.Nanosecond,
	}, fila, repo, func(context.Context, uuid.UUID) error { return nil }, semLog())

	n, err := d.RecuperarOrfaos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recuperou %d orfas, queria 1", n)
	}

	depois, err := repo.Buscar(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if depois.Status != dom.StatusQueued {
		t.Errorf("run ficou em %s; queria queued, pronta para outro worker", depois.Status)
	}
	if depois.Attempt != 1 {
		t.Errorf("tentativa = %d; a morte do worker consome uma tentativa", depois.Attempt)
	}
	if depois.Err == "" {
		t.Error("o run precisa registrar POR QUE foi recuperado")
	}
	if pendentes, _, _ := fila.Tamanho(ctx); pendentes != 1 {
		t.Error("o item deveria estar livre para outro worker")
	}
}

// With the attempts exhausted, the orphan stops at failed instead of circling
// between workers forever.
func TestOrfaoParaDeVoltarQuandoEsgotaTentativas(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "orfa2", Definition: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	d := scheduler.New(scheduler.Config{
		Worker: "vivo", MaxTentativas: 1, Visibilidade: time.Nanosecond,
	}, fila, repo, func(context.Context, uuid.UUID) error { return nil }, semLog())

	if _, err := fila.Claim(ctx, "morto", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecuperarOrfaos(ctx); err != nil {
		t.Fatal(err)
	}

	depois, _ := repo.Buscar(ctx, r.ID)
	if depois.Status != dom.StatusFailed {
		t.Errorf("run em %s; com tentativas esgotadas tem de parar em failed", depois.Status)
	}
	if pendentes, reivindicados, _ := fila.Tamanho(ctx); pendentes+reivindicados != 0 {
		t.Errorf("fila com %d itens; o orfao esgotado tem de sair dela", pendentes+reivindicados)
	}
}

type alertaFalso struct {
	mu       sync.Mutex
	recebido []notify.Alerta
	erro     error
}

func (a *alertaFalso) Falhou(_ context.Context, al notify.Alerta) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recebido = append(a.recebido, al)
	return a.erro
}

func (a *alertaFalso) total() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.recebido)
}

// The alert fires ONCE, when the run gives up -- not on every attempt.
// Announcing every failure would turn a successful retry into two alerts and a
// silence, and a channel that shouts for nothing stops being read.
func TestAlertaSaiUmaVezQuandoEsgotamAsTentativas(t *testing.T) {
	pool := banco(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "falha",
		TriggerType: "schedule", Definition: []byte(`{"Tags":["acme","id","dbt"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	avisos := &alertaFalso{}
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxTentativas: 3,
		Intervalo: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, fila, repo, func(context.Context, uuid.UUID) error {
		return errors.New(`step "run": saiu com codigo 2`)
	}, semLog())
	d.Alertas = avisos
	d.URLBase = "https://brevis.example.com"

	go func() { _ = d.Run(ctx) }()

	// Espera o run esgotar as tentativas.
	prazo := time.Now().Add(15 * time.Second)
	for time.Now().Before(prazo) {
		atual, _ := repo.Buscar(ctx, r.ID)
		if atual.Status == dom.StatusFailed && atual.Attempt >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)

	if n := avisos.total(); n != 1 {
		t.Fatalf("%d alertas para um run que falhou uma vez; quero exatamente 1", n)
	}

	a := avisos.recebido[0]
	if a.Workflow != "id_verification" || a.Trigger != "schedule" {
		t.Errorf("alerta sem os detalhes do run: %+v", a)
	}
	if len(a.Tags) != 3 || a.Tags[1] != "id" {
		t.Errorf("tags do snapshot nao chegaram: %v", a.Tags)
	}
	if !strings.Contains(a.Err, "codigo 2") {
		t.Errorf("alerta sem a causa: %q", a.Err)
	}
	if a.URLBase == "" || a.RunID != r.ID.String() {
		t.Errorf("alerta sem o link da execucao: %+v", a)
	}
}

// A webhook that is down must not stop the dispatcher: the run has to end FAILED
// and the queue has to keep being consumed.
func TestFalhaAoAvisarNaoDerrubaODispatcher(t *testing.T) {
	pool := banco(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, _ := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "x", Definition: []byte(`{}`),
	})
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = fila.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxTentativas: 1,
		Intervalo: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, fila, repo, func(context.Context, uuid.UUID) error {
		return errors.New("falhou")
	}, semLog())
	d.Alertas = &alertaFalso{erro: errors.New("slack respondeu 500")}

	go func() { _ = d.Run(ctx) }()

	prazo := time.Now().Add(10 * time.Second)
	for time.Now().Before(prazo) {
		atual, _ := repo.Buscar(ctx, r.ID)
		if atual.Status == dom.StatusFailed {
			cancel()
			if pendentes, reivindicados, _ := fila.Tamanho(ctx); pendentes+reivindicados != 0 {
				t.Errorf("item ficou preso na fila apos o alerta falhar")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("o run nao terminou: o erro do alerta travou o dispatcher")
}

func enfileirar(t *testing.T, repo *postgres.RunRepo, fila *queue.Queue,
	slug string, quantos, maxAtivos int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < quantos; i++ {
		r, err := repo.Criar(ctx, dom.Run{
			WorkflowSlug: slug, IdempotencyKey: fmt.Sprintf("%s-%d", slug, i),
			Definition: []byte(`{}`), MaxActive: maxAtivos,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

// The case that motivated all of it: a `*/15` that takes 20 minutes overlaps
// itself, and two `dbt build`s on the SAME model fight over the same table.
//
// Five items of the same workflow with a limit of 1: the claim hands out ONE,
// however many global slots there are.
func TestLimitePorWorkflowSegurraOsDemais(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "id_verification_today", 5, 1)

	itens, err := fila.Claim(ctx, "w", 10) // dez vagas globais
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 1 {
		t.Fatalf("claim entregou %d itens; o limite do workflow e 1", len(itens))
	}

	// Until the first finishes, nobody else goes in.
	outros, err := fila.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outros) != 0 {
		t.Errorf("entregou mais %d com o primeiro ainda em voo", len(outros))
	}

	// Terminado o primeiro, o proximo entra.
	if err := fila.Done(ctx, itens[0].ID); err != nil {
		t.Fatal(err)
	}
	seguintes, err := fila.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(seguintes) != 1 {
		t.Errorf("apos liberar a vaga, o claim entregou %d; queria 1", len(seguintes))
	}
}

// A limit greater than 1 hands out exactly the limit -- no fewer (which would
// be
// serializacao), nem mais.
func TestLimiteDeTresEntregaTres(t *testing.T) {
	pool := banco(t)
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "vendors_x", 8, 3)

	itens, err := fila.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 3 {
		t.Errorf("claim entregou %d; o limite e 3", len(itens))
	}
}

// A workflow at its limit must not block the others: the queue is shared, and
// stalling everything because of one would be worse than having no limit.
func TestWorkflowNoLimiteNaoBloqueiaOsOutros(t *testing.T) {
	pool := banco(t)
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "travado", 5, 1)
	enfileirar(t, repo, fila, "livre_a", 2, 0)
	enfileirar(t, repo, fila, "livre_b", 2, 0)

	itens, err := fila.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	// 1 from the blocked one + 4 from the free ones.
	if len(itens) != 5 {
		t.Errorf("claim entregou %d; queria 5 (1 limitado + 4 sem limite)", len(itens))
	}
}

// Sem limite declarado (0), nada muda — o comportamento antigo continua sendo o
// padrao.
func TestSemLimiteEntregaTudoQueCabe(t *testing.T) {
	pool := banco(t)
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "sem_limite", 6, 0)

	itens, err := fila.Claim(context.Background(), "w", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 4 {
		t.Errorf("claim entregou %d; a vaga global era 4", len(itens))
	}
}

// A run that fails and passes on the second attempt does NOT alert.
//
// It is the rule's other half: the alert exists for a definitive failure.
// Announcing a failure the retry itself fixed trains the team to ignore the
// channel, and then the alert that matters goes past unnoticed with it.
func TestRetryQueDaCertoNaoAlerta(t *testing.T) {
	pool := banco(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "retry-ok",
		TriggerType: "schedule", Definition: []byte(`{"Tags":["acme","id"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	avisos := &alertaFalso{}
	var chamadas int32
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxTentativas: 3,
		Intervalo: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, fila, repo, func(context.Context, uuid.UUID) error {
		if atomic.AddInt32(&chamadas, 1) == 1 {
			return errors.New(`step "run": saiu com codigo 2`)
		}
		return nil // the second attempt passes
	}, semLog())
	d.Alertas = avisos

	go func() { _ = d.Run(ctx) }()

	prazo := time.Now().Add(15 * time.Second)
	for time.Now().Before(prazo) {
		if atual, _ := repo.Buscar(ctx, r.ID); atual.Status == dom.StatusSuccess {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	time.Sleep(150 * time.Millisecond)

	if atual, _ := repo.Buscar(context.Background(), r.ID); atual.Status != dom.StatusSuccess {
		t.Fatalf("o run terminou como %s; o teste precisa que ele passe na segunda", atual.Status)
	}
	if n := avisos.total(); n != 0 {
		t.Errorf("saiu %d alerta(s) para um run que se recuperou sozinho", n)
	}
}

// The alert has to name the step and carry the end of that step's log.
//
// Without this it only says something failed, and whoever is on call at 4am
// opens the screen to find out what -- which is exactly the work the alert was
// supposed to
// poupar.
func TestAlertaCarregaOPassoEOLog(t *testing.T) {
	pool := banco(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "vendors_inmet_observation", IdempotencyKey: "com-log",
		TriggerType: "schedule", Definition: []byte(`{"Tags":["acme","vendors"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	avisos := &alertaFalso{}
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxTentativas: 1,
		Intervalo: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, fila, repo, func(ctx context.Context, id uuid.UUID) error {
		// Writes the task the way the runner would, with output.
		if err := repo.IniciarTask(ctx, id, "fetch_observations", 0); err != nil {
			return err
		}
		saida := "conectando na api do inmet\nHTTP 503 Service Unavailable\ndesistindo apos 3 tentativas"
		codigo := 1
		if err := repo.TerminarTask(ctx, id, "fetch_observations", 0,
			dom.StatusFailed, &codigo, "saiu com codigo 1", saida); err != nil {
			return err
		}
		return errors.New(`step "fetch_observations": saiu com codigo 1`)
	}, semLog())
	d.Alertas = avisos

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
	if len(avisos.recebido) == 0 {
		t.Fatal("nenhum alerta saiu")
	}
	a := avisos.recebido[0]
	if a.Passo != "fetch_observations" {
		t.Errorf("Passo = %q; esperava o node que falhou", a.Passo)
	}
	if !strings.Contains(a.TrechoDoLog, "503 Service Unavailable") {
		t.Errorf("TrechoDoLog nao traz a causa: %q", a.TrechoDoLog)
	}
}
