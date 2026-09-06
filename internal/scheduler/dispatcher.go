// Package scheduler holds the dispatcher from §27 of the plan.
//
// The design is §8's: a PERSISTENT queue (Postgres) plus an IN-MEMORY
// dispatcher. The dispatcher holds no work -- it claims from the queue, runs,
// and returns the result. If the process dies, the claimed items become free
// again through `Recuperar`, and nothing is lost.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/notify"
	"github.com/AreteAcademy/brevis/internal/queue"
)

// Executar runs a Run. Injected so the dispatcher knows nothing of executors,
// graphs or YAML -- which makes the concurrency testable without a real
// process.
type Executar func(ctx context.Context, runID uuid.UUID) error

// Repo is what the dispatcher needs from persistence. A small interface,
// declared here in the consumer rather than in the package that implements
// it.
type Repo interface {
	Transicionar(ctx context.Context, id uuid.UUID, para dom.Status) error
	IncrementarTentativa(ctx context.Context, id uuid.UUID) (int, error)
	RegistrarErro(ctx context.Context, id uuid.UUID, msg string) error
	Buscar(ctx context.Context, id uuid.UUID) (dom.Run, error)

	// PassoQueFalhou returns the node and the output of the last failed
	// attempt. It feeds the alert: without it the alert says something failed,
	// and whoever is on call has to open the screen to find out what.
	PassoQueFalhou(ctx context.Context, id uuid.UUID) (passo, log string, err error)
}

// Config parameterises the dispatcher.
type Config struct {
	Worker         string
	MaxConcorrente int
	Intervalo      time.Duration
	MaxTentativas  int
	BackoffBase    time.Duration

	// Visibilidade is how long an item may stay claimed without the worker
	// finishing before it counts as orphaned. It has to be LONGER than the
	// longest expected run: too short, and the dispatcher steals from itself a
	// run that is still going.
	Visibilidade time.Duration

	// IntervaloRecuperacao is how often the orphan sweep runs.
	IntervaloRecuperacao time.Duration
}

func (c *Config) padroes() {
	if c.Worker == "" {
		c.Worker = "dispatcher"
	}
	if c.MaxConcorrente <= 0 {
		c.MaxConcorrente = 5
	}
	if c.Intervalo <= 0 {
		c.Intervalo = 200 * time.Millisecond
	}
	if c.MaxTentativas <= 0 {
		c.MaxTentativas = 3
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.Visibilidade <= 0 {
		c.Visibilidade = 15 * time.Minute
	}
	if c.IntervaloRecuperacao <= 0 {
		c.IntervaloRecuperacao = time.Minute
	}
}

// Dispatcher drains the queue while respecting the maximum concurrency.
type Dispatcher struct {
	cfg      Config
	fila     *queue.Queue
	repo     Repo
	executar Executar
	log      *slog.Logger

	// Alertas warns when a run gives up. Nil means nobody is told.
	Alertas notify.Notificador

	// URLBase of the UI, for the link in the alert.
	URLBase string

	mu    sync.Mutex
	emVoo int
	wg    sync.WaitGroup
}

func New(cfg Config, f *queue.Queue, r Repo, e Executar, log *slog.Logger) *Dispatcher {
	cfg.padroes()
	return &Dispatcher{cfg: cfg, fila: f, repo: r, executar: e, log: log}
}

// Run drains the queue until the context is cancelled, then waits for in-flight
// work to finish before returning.
func (d *Dispatcher) Run(ctx context.Context) error {
	tick := time.NewTicker(d.cfg.Intervalo)
	defer tick.Stop()

	// The orphan sweep runs on a ticker of its own, far slower than the claim
	// one: it is a safety net, not a hot path.
	recuperacao := time.NewTicker(d.cfg.IntervaloRecuperacao)
	defer recuperacao.Stop()

	for {
		select {
		case <-ctx.Done():
			d.wg.Wait() // shutdown gracioso: nao abandona execucao em voo
			return nil
		case <-tick.C:
			if err := d.cicloDeClaim(ctx); err != nil {
				d.log.Error("ciclo de claim", "erro", err)
			}
		case <-recuperacao.C:
			if n, err := d.RecuperarOrfaos(ctx); err != nil {
				d.log.Error("recuperando orfaos", "erro", err)
			} else if n > 0 {
				d.log.Warn("runs orfas recuperadas", "quantidade", n)
			}
		}
	}
}

// RecuperarOrfaos returns to the queue what got stuck in a worker that died.
//
// It was the failure mode left open since PHASE 2: `Queue.Recuperar` existed
// and nobody called it. In practice, killing the process mid-run left the item
// claimed forever AND the Run "running" forever -- the screen showed work in
// progress that no longer existed.
//
// An orphan is treated as a FAILURE of that attempt, and not as a direct
// requeue, for two reasons: the state machine has no running -> queued edge
// (§7), and a worker that dies midway did consume a real attempt -- counting it
// is what stops a poisonous run from taking down workers in a loop.
func (d *Dispatcher) RecuperarOrfaos(ctx context.Context) (int, error) {
	itens, err := d.fila.Recuperar(ctx, d.cfg.Visibilidade)
	if err != nil {
		return 0, err
	}
	for _, it := range itens {
		d.falhar(ctx, it, errOrfao{worker: d.cfg.Worker, limite: d.cfg.Visibilidade})
	}
	return len(itens), nil
}

// errOrfao explains in its own message why the run failed -- it is the text the
// operator reads on screen, and "unknown error" there costs a whole
// investigation.
type errOrfao struct {
	worker string
	limite time.Duration
}

func (e errOrfao) Error() string {
	return fmt.Sprintf("execucao orfa: nenhum worker deu sinal em %s "+
		"(o processo que a reivindicou provavelmente caiu)", e.limite)
}

// cicloDeClaim pede a fila APENAS as vagas livres.
//
// This is where concurrency is enforced, and why it is reliable: there is no
// path in which more items leave the queue than the limit allows, because
// whoever counts the slots is whoever makes the request. A semaphore after the
// claim would leave items claimed and idle, invisible to other workers.
func (d *Dispatcher) cicloDeClaim(ctx context.Context) error {
	d.mu.Lock()
	vagas := d.cfg.MaxConcorrente - d.emVoo
	d.mu.Unlock()

	if vagas <= 0 {
		return nil
	}

	itens, err := d.fila.Claim(ctx, d.cfg.Worker, vagas)
	if err != nil {
		return err
	}

	for _, it := range itens {
		d.mu.Lock()
		d.emVoo++
		d.mu.Unlock()

		d.wg.Add(1)
		go func(it queue.Item) {
			defer d.wg.Done()
			defer func() {
				d.mu.Lock()
				d.emVoo--
				d.mu.Unlock()
			}()
			d.processar(ctx, it)
		}(it)
	}
	return nil
}

func (d *Dispatcher) processar(ctx context.Context, it queue.Item) {
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRunning); err != nil {
		// An invalid transition here means another dispatcher took the same run,
		// or that it was cancelled. Not our error: release and move on.
		d.log.Warn("nao pude marcar running", "run", it.RunID, "erro", err)
		_ = d.fila.Release(ctx, it.ID, 0)
		return
	}

	err := d.executar(ctx, it.RunID)
	if err == nil {
		if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusSuccess); err != nil {
			d.log.Error("marcando success", "run", it.RunID, "erro", err)
		}
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	d.falhar(ctx, it, err)
}

// falhar decide entre retry e desistencia.
func (d *Dispatcher) falhar(ctx context.Context, it queue.Item, causa error) {
	_ = d.repo.RegistrarErro(ctx, it.RunID, causa.Error())
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusFailed); err != nil {
		d.log.Error("marcando failed", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	tentativa, err := d.repo.IncrementarTentativa(ctx, it.RunID)
	if err != nil {
		d.log.Error("incrementando tentativa", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	if tentativa >= d.cfg.MaxTentativas {
		// Exhausted: leaves the queue and stays FAILED, which is not terminal in
		// the state machine but is the end of this run.
		d.log.Warn("tentativas esgotadas", "run", it.RunID, "tentativas", tentativa)
		// The alert goes out HERE, and not on every failure: warning on every
		// attempt would turn a successful retry into two alerts and a silence,
		// and a channel that cries wolf stops being read.
		d.avisar(ctx, it.RunID, tentativa, causa)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRetrying); err != nil {
		d.log.Error("marcando retrying", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusQueued); err != nil {
		d.log.Error("reenfileirando", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	// Exponential backoff. The item returns to the queue delayed, not at once:
	// an instant retry against a dependency that is down only burns the
	// queue.
	atraso := d.cfg.BackoffBase * time.Duration(1<<uint(tentativa-1))
	d.log.Info("reenfileirado", "run", it.RunID, "tentativa", tentativa, "atraso", atraso)
	if err := d.fila.Release(ctx, it.ID, atraso); err != nil {
		d.log.Error("devolvendo a fila", "run", it.RunID, "erro", err)
	}
}

// avisar manda o alerta de falha definitiva.
//
// Nothing here may interrupt the dispatcher: a webhook that is down is no
// reason to stop draining the queue. A failure to warn becomes a log line, and
// the run's state in the database remains the source of truth.
func (d *Dispatcher) avisar(ctx context.Context, runID uuid.UUID, tentativas int, causa error) {
	if d.Alertas == nil {
		return
	}

	a := notify.Alerta{
		RunID: runID.String(), Status: string(dom.StatusFailed),
		Tentativas: tentativas, Erro: causa.Error(), URLBase: d.URLBase,
	}
	// Os detalhes vem do banco: o dispatcher so conhece o id. Se a leitura
	// falhar, o alerta sai mesmo assim — meia mensagem e melhor que nenhuma
	// quando algo esta quebrado.
	if r, err := d.repo.Buscar(ctx, runID); err == nil {
		a.Workflow, a.Trigger, a.LogicalDate = r.WorkflowSlug, r.TriggerType, r.LogicalDate
		var def struct{ Tags []string }
		if json.Unmarshal(r.Definicao, &def) == nil {
			a.Tags = def.Tags
		}
	} else {
		d.log.Warn("alerta sem detalhes do run", "run", runID, "erro", err)
	}

	// O passo e o log sao um plus: se a consulta falhar, o alerta sai sem eles.
	// Meia mensagem chega; mensagem nenhuma, nao.
	if passo, log, err := d.repo.PassoQueFalhou(ctx, runID); err == nil {
		a.Passo = passo
		a.TrechoDoLog = ultimasLinhas(log, 15)
	} else {
		d.log.Warn("alerta sem o passo que falhou", "run", runID, "erro", err)
	}

	// Contexto proprio: o da execucao pode estar cancelado (foi o cancelamento
	// que trouxe ate aqui), e o alerta e justamente sobre isso.
	ctxAviso, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Alertas.Falhou(ctxAviso, a); err != nil {
		d.log.Error("nao consegui avisar da falha", "run", runID, "erro", err)
	}
}

// ultimasLinhas devolve o FIM do log, que e onde um programa costuma dizer por
// que parou. O comeco fica de fora de proposito: o alerta cabe numa notificacao
// de celular, e o log inteiro esta a um clique de distancia na tela da execucao.
func ultimasLinhas(texto string, n int) string {
	if texto == "" {
		return ""
	}
	linhas := strings.Split(strings.TrimRight(texto, "\n"), "\n")
	if len(linhas) > n {
		linhas = linhas[len(linhas)-n:]
	}
	return strings.Join(linhas, "\n")
}

// EmVoo devolve quantas execucoes estao correndo agora.
func (d *Dispatcher) EmVoo() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.emVoo
}
