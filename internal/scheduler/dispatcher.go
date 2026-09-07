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
	IncrementAttempt(ctx context.Context, id uuid.UUID) (int, error)
	RecordError(ctx context.Context, id uuid.UUID, msg string) error
	Get(ctx context.Context, id uuid.UUID) (dom.Run, error)

	// FailedStep returns the node and the output of the last failed
	// attempt. It feeds the alert: without it the alert says something failed,
	// and whoever is on call has to open the screen to find out what.
	FailedStep(ctx context.Context, id uuid.UUID) (step, log string, err error)
}

// Config parameterises the dispatcher.
type Config struct {
	Worker         string
	MaxConcorrente int
	Interval       time.Duration
	MaxAttempts    int
	BackoffBase    time.Duration

	// Visibility is how long an item may stay claimed without the worker
	// finishing before it counts as orphaned. It has to be LONGER than the
	// longest expected run: too short, and the dispatcher steals from itself a
	// run that is still going.
	Visibility time.Duration

	// RecoveryInterval is how often the orphan sweep runs.
	RecoveryInterval time.Duration
}

func (c *Config) defaults() {
	if c.Worker == "" {
		c.Worker = "dispatcher"
	}
	if c.MaxConcorrente <= 0 {
		c.MaxConcorrente = 5
	}
	if c.Interval <= 0 {
		c.Interval = 200 * time.Millisecond
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.Visibility <= 0 {
		c.Visibility = 15 * time.Minute
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = time.Minute
	}
}

// Dispatcher drains the queue while respecting the maximum concurrency.
type Dispatcher struct {
	cfg      Config
	queue    *queue.Queue
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
	cfg.defaults()
	return &Dispatcher{cfg: cfg, queue: f, repo: r, executar: e, log: log}
}

// Run drains the queue until the context is cancelled, then waits for in-flight
// work to finish before returning.
func (d *Dispatcher) Run(ctx context.Context) error {
	tick := time.NewTicker(d.cfg.Interval)
	defer tick.Stop()

	// The orphan sweep runs on a ticker of its own, far slower than the claim
	// one: it is a safety net, not a hot path.
	recovery := time.NewTicker(d.cfg.RecoveryInterval)
	defer recovery.Stop()

	for {
		select {
		case <-ctx.Done():
			d.wg.Wait() // graceful shutdown: it does not abandon a run in flight
			return nil
		case <-tick.C:
			if err := d.claimCycle(ctx); err != nil {
				d.log.Error("claim cycle", "error", err)
			}
		case <-recovery.C:
			if n, err := d.RecuperarOrfaos(ctx); err != nil {
				d.log.Error("recovering orphans", "error", err)
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
	items, err := d.queue.Recuperar(ctx, d.cfg.Visibility)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		d.fail(ctx, it, errOrphan{worker: d.cfg.Worker, limite: d.cfg.Visibility})
	}
	return len(items), nil
}

// errOrphan explains in its own message why the run failed -- it is the text the
// operator reads on screen, and "unknown error" there costs a whole
// investigation.
type errOrphan struct {
	worker string
	limite time.Duration
}

func (e errOrphan) Error() string {
	return fmt.Sprintf("orphaned run: no worker reported in for %s "+
		"(the process that claimed it most likely died)", e.limite)
}

// claimCycle asks the queue for the free slots ONLY.
//
// This is where concurrency is enforced, and why it is reliable: there is no
// path in which more items leave the queue than the limit allows, because
// whoever counts the slots is whoever makes the request. A semaphore after the
// claim would leave items claimed and idle, invisible to other workers.
func (d *Dispatcher) claimCycle(ctx context.Context) error {
	d.mu.Lock()
	slots := d.cfg.MaxConcorrente - d.emVoo
	d.mu.Unlock()

	if slots <= 0 {
		return nil
	}

	items, err := d.queue.Claim(ctx, d.cfg.Worker, slots)
	if err != nil {
		return err
	}

	for _, it := range items {
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
			d.process(ctx, it)
		}(it)
	}
	return nil
}

func (d *Dispatcher) process(ctx context.Context, it queue.Item) {
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRunning); err != nil {
		// An invalid transition here means another dispatcher took the same run,
		// or that it was cancelled. Not our error: release and move on.
		d.log.Warn("could not mark running", "run", it.RunID, "error", err)
		_ = d.queue.Release(ctx, it.ID, 0)
		return
	}

	err := d.executar(ctx, it.RunID)
	if err == nil {
		if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusSuccess); err != nil {
			d.log.Error("marking success", "run", it.RunID, "error", err)
		}
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	d.fail(ctx, it, err)
}

// falhar decide entre retry e desistencia.
func (d *Dispatcher) fail(ctx context.Context, it queue.Item, causa error) {
	_ = d.repo.RecordError(ctx, it.RunID, causa.Error())
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusFailed); err != nil {
		d.log.Error("marking failed", "run", it.RunID, "error", err)
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	attempt, err := d.repo.IncrementAttempt(ctx, it.RunID)
	if err != nil {
		d.log.Error("incrementing the attempt", "run", it.RunID, "error", err)
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	if attempt >= d.cfg.MaxAttempts {
		// Exhausted: leaves the queue and stays FAILED, which is not terminal in
		// the state machine but is the end of this run.
		d.log.Warn("out of attempts", "run", it.RunID, "attempts", attempt)
		// The alert goes out HERE, and not on every failure: warning on every
		// attempt would turn a successful retry into two alerts and a silence,
		// and a channel that cries wolf stops being read.
		d.avisar(ctx, it.RunID, attempt, causa)
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRetrying); err != nil {
		d.log.Error("marking retrying", "run", it.RunID, "error", err)
		_ = d.queue.Done(ctx, it.ID)
		return
	}
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusQueued); err != nil {
		d.log.Error("requeuing", "run", it.RunID, "error", err)
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	// Exponential backoff. The item returns to the queue delayed, not at once:
	// an instant retry against a dependency that is down only burns the
	// queue.
	atraso := d.cfg.BackoffBase * time.Duration(1<<uint(attempt-1))
	d.log.Info("requeued", "run", it.RunID, "attempt", attempt, "delay", atraso)
	if err := d.queue.Release(ctx, it.ID, atraso); err != nil {
		d.log.Error("handing back to the queue", "run", it.RunID, "error", err)
	}
}

// avisar sends the definitive-failure alert.
//
// Nothing here may interrupt the dispatcher: a webhook that is down is no
// reason to stop draining the queue. A failure to warn becomes a log line, and
// the run's state in the database remains the source of truth.
func (d *Dispatcher) avisar(ctx context.Context, runID uuid.UUID, attempts int, causa error) {
	if d.Alertas == nil {
		return
	}

	a := notify.Alerta{
		RunID: runID.String(), Status: string(dom.StatusFailed),
		Tentativas: attempts, Err: causa.Error(), URLBase: d.URLBase,
	}
	// Os detalhes vem do banco: o dispatcher so conhece o id. Se a leitura
	// fails, the alert goes out anyway — half a message beats none when
	// something is broken.
	if r, err := d.repo.Get(ctx, runID); err == nil {
		a.Workflow, a.Trigger, a.LogicalDate = r.WorkflowSlug, r.TriggerType, r.LogicalDate
		var def struct{ Tags []string }
		if json.Unmarshal(r.Definition, &def) == nil {
			a.Tags = def.Tags
		}
	} else {
		d.log.Warn("alert without the run's details", "run", runID, "error", err)
	}

	// The step and the log are a bonus: if the query fails, the alert goes out
	// without them. Half a message arrives; no message does not.
	if step, log, err := d.repo.FailedStep(ctx, runID); err == nil {
		a.Passo = step
		a.TrechoDoLog = lastLines(log, 15)
	} else {
		d.log.Warn("alert without the step that failed", "run", runID, "error", err)
	}

	// A context of its own: the run's may be cancelled (the cancellation is what
	// brought us here), and the alert is about exactly that.
	ctxAviso, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Alertas.Falhou(ctxAviso, a); err != nil {
		d.log.Error("could not announce the failure", "run", runID, "error", err)
	}
}

// lastLines returns the END of the log, which is where a program usually
// says why it stopped. The start is left out on purpose: the alert has to fit in
// a notification
// on a phone, and the whole log is one click away on the run's screen.
func lastLines(text string, n int) string {
	if text == "" {
		return ""
	}
	rows := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

// EmVoo devolve quantas execucoes estao correndo agora.
func (d *Dispatcher) EmVoo() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.emVoo
}
