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

	"github.com/AreteAcademy/brevis/internal/alerts"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	sch "github.com/AreteAcademy/brevis/internal/domain/schedule"
	wfdom "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/notify"
	"github.com/AreteAcademy/brevis/internal/observability/metrics"
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
	RecordError(ctx context.Context, id uuid.UUID, msg string) error
	Get(ctx context.Context, id uuid.UUID) (dom.Run, error)

	// Attempt spends an attempt and, when it was the last, writes the alert in
	// the SAME transaction. The two cannot come apart, which is the point --
	// see the postgres implementation.
	Attempt(ctx context.Context, id uuid.UUID, budget int,
		raise func(attempt int, gaveUp bool) []alerts.Pending) (attempt int, gaveUp bool, err error)

	// RecordAuto works out the run's automatic params and stores them. Called
	// once the run is RUNNING, because `started_at` is one of them, and again
	// on every retry, because a retry moves it.
	RecordAuto(ctx context.Context, id uuid.UUID,
		window func(slot time.Time) (start, end time.Time)) (dom.AutoParams, error)

	// FailedStep returns the node and the output of the last failed
	// attempt. It feeds the alert: without it the alert says something failed,
	// and whoever is on call has to open the screen to find out what.
	FailedStep(ctx context.Context, id uuid.UUID) (step, log string, err error)

	// FailedSteps returns every failed node with its log, for the steps that
	// declared `on_error`. A run with two parallel branches can have two.
	FailedSteps(ctx context.Context, id uuid.UUID) (map[string]string, error)
}

// Config parameterises the dispatcher.
type Config struct {
	Worker         string
	MaxConcorrente int
	Interval       time.Duration

	// MaxAttempts is how many times a failed RUN is tried, counting the first.
	// The default is 3.
	MaxAttempts int

	// BackoffBase is the first delay, doubled on every attempt after it. With
	// the default of 30s and three attempts, they land at 0s, 30s and 1m30s.
	//
	// It used to be one second, which put the three attempts inside three
	// seconds -- and for the failure these pipelines actually have, a
	// rate-limited vendor API, that is indistinguishable from no retry at all:
	// the limiter sees all three inside its own window and rejects all three.
	// A consumer migrating 34 tasks that each asked for three attempts THREE
	// MINUTES apart reported it, which is what moved this number.
	//
	// 30 seconds rather than their three minutes, because the number is a
	// platform default and not one installation's: it is the largest value
	// that keeps a definitive failure's alert inside two minutes, and the
	// smallest where the third attempt lands outside a one-minute rate-limit
	// window. Whoever needs their number sets --retry-backoff.
	BackoffBase time.Duration

	// BackoffMax caps the delay, and it is also what makes the arithmetic
	// safe. Without a ceiling the exponential is unbounded, and that became
	// reachable the moment MaxAttempts got a flag: --max-attempts 10
	// --retry-backoff 60s makes the last wait four hours and the whole window
	// eight, which nobody asks for and everybody could type. Past that it
	// overflows int64 and comes back negative, then zero -- see backoff.
	//
	// It never binds at the defaults -- three attempts reach 60s -- so it costs
	// nothing until somebody raises the attempts, which is exactly when it is
	// wanted. Airflow calls it max_retry_delay and Temporal maximumInterval;
	// this is the same knob under the name the other two flags use.
	BackoffMax time.Duration

	// Visibility is how long an item may stay claimed without the worker
	// finishing before it counts as orphaned. It has to be LONGER than the
	// longest expected run: too short, and the dispatcher steals from itself a
	// run that is still going.
	Visibility time.Duration

	// RecoveryInterval is how often the orphan sweep runs.
	RecoveryInterval time.Duration
}

// Defaults is the policy a Config left at zero gets.
//
// It is exported so `scheduler --help` can print the same numbers the
// dispatcher uses, instead of repeating them as literals in cmd/brevis. That
// duplication is the shape this repository has been bitten by four times -- the
// node type, the phase offset, the pill width, the image pins -- and the fix
// that works is removing the second place, not gating it.
func Defaults() Config {
	var c Config
	c.defaults()
	return c
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
		c.BackoffBase = 30 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = time.Hour
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

	// Channel is where a run-level alert goes. Empty means alerting is off and
	// no row is written at all -- an outbox filling up with alerts nobody can
	// deliver would be worse than none.
	//
	// The dispatcher no longer TALKS to Slack, and that is the change: it
	// writes a row, and the alert pod delivers it. A webhook that is down is
	// now something the delivery process retries rather than something this
	// process logs and forgets.
	Channel string

	// Metrics is the scrape's source. Nil means nothing is measured, which is
	// the case for `brevis run` and for every test here -- the package
	// tolerates it rather than requiring a no-op to be threaded through.
	Metrics *metrics.Metrics

	// BaseURL of the UI, for the link in the alert.
	BaseURL string

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
			if n, err := d.RecoverOrphans(ctx); err != nil {
				d.log.Error("recovering orphans", "error", err)
			} else if n > 0 {
				d.log.Warn("runs orfas recuperadas", "quantidade", n)
			}
		}
	}
}

// RecoverOrphans returns to the queue what got stuck in a worker that died.
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
func (d *Dispatcher) RecoverOrphans(ctx context.Context) (int, error) {
	items, err := d.queue.Recover(ctx, d.cfg.Visibility)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		d.fail(ctx, it, errOrphan{worker: d.cfg.Worker, limite: d.cfg.Visibility})
	}
	// Until now a dead worker was a log line and nothing else, so "workers are
	// dying" was not a thing anybody could alert on.
	d.Metrics.OrphansRecovered(ctx, len(items))
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
		// Enqueued to claimed, which is the dispatcher's own SLA. It is
		// measured from disponivel_em and not from the row's creation on
		// purpose: an item waiting out its retry backoff is not the scheduler
		// being slow, and counting it would make a healthy backoff look like
		// saturation.
		//
		// Clamped at zero because the two clocks are different. disponivel_em
		// is the DATABASE's now(); time.Now() here is the process's, and a few
		// milliseconds of skew would otherwise land negative observations in
		// the first bucket.
		if waited := time.Since(it.AvailableAt); waited > 0 {
			d.Metrics.Claimed(ctx, waited)
		} else {
			d.Metrics.Claimed(ctx, 0)
		}

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

	// The automatic params are settled BEFORE the run executes, because the
	// steps read them. On a retry this runs again: `started_at` moved and the
	// previous run may have changed, while `adjusted_at` stays pinned to the
	// slot -- which is what makes a retried run read the same window as the
	// attempt that failed.
	if _, err := d.repo.RecordAuto(ctx, it.RunID, d.window(ctx, it.RunID)); err != nil {
		// Not fatal. A run without them still runs; its steps fall back to
		// their own clock, which is what every pipeline did before this
		// existed. Failing the run over a convenience would be the worse
		// trade.
		d.log.Warn("automatic params not recorded", "run", it.RunID, "error", err)
	}

	err := d.executar(ctx, it.RunID)

	// From here on the bookkeeping runs on a context of its own. See settle.
	done, cancel := settle()
	defer cancel()

	if err == nil {
		if err := d.repo.Transicionar(done, it.RunID, dom.StatusSuccess); err != nil {
			d.log.Error("marking success", "run", it.RunID, "error", err)
		}
		// The queue item is released BEFORE the run is measured. Measuring is
		// bookkeeping and it costs a query; leaving an item claimed while it
		// happens widens the window in which a crash strands the item, for no
		// gain.
		_ = d.queue.Done(done, it.ID)
		d.measure(done, it.RunID, dom.StatusSuccess)
		return
	}

	d.fail(done, it, err)
}

// settle returns the context for everything that happens AFTER a run finishes.
//
// The run's own context is usually already cancelled by the time this matters:
// a SIGTERM is what ended the run, and every database call made with it then
// fails silently. Run() waits for in-flight work before returning, which is
// pointless if the work it waits for cannot write anything down.
//
// The symptom is a run marked SUCCESS whose queue item was never deleted. It
// stays claimed until the visibility sweep returns it fifteen minutes later,
// and the sweep counts it as a FAILED attempt -- of a run that succeeded.
//
// Fifteen seconds, which fits inside a normal terminationGracePeriodSeconds.
// This is not "ignore the shutdown", it is "finish writing down what already
// happened".
func settle() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// falhar decide entre retry e desistencia.
func (d *Dispatcher) fail(ctx context.Context, it queue.Item, cause error) {
	_ = d.repo.RecordError(ctx, it.RunID, cause.Error())
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusFailed); err != nil {
		d.log.Error("marking failed", "run", it.RunID, "error", err)
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	// The attempt and the alert are one write. The alert is raised HERE, on the
	// last attempt only: warning on every attempt would turn a run that
	// recovers on its second try into two alerts and a silence, and a channel
	// that cries wolf stops being read.
	attempt, gaveUp, err := d.repo.Attempt(ctx, it.RunID, d.cfg.MaxAttempts,
		d.raise(ctx, it.RunID, cause))
	if err != nil {
		d.log.Error("spending the attempt", "run", it.RunID, "error", err)
		_ = d.queue.Done(ctx, it.ID)
		return
	}

	if gaveUp {
		// Exhausted: leaves the queue and stays FAILED, which is not terminal in
		// the state machine but is the end of this run.
		d.log.Warn("out of attempts", "run", it.RunID, "attempts", attempt)
		d.measure(ctx, it.RunID, dom.StatusFailed)
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

	// Exponential backoff, capped. The item returns to the queue delayed, not
	// at once: an instant retry against a dependency that is down only burns
	// the queue.
	atraso := d.backoff(attempt)
	d.log.Info("requeued", "run", it.RunID, "attempt", attempt, "delay", atraso)
	if err := d.queue.Release(ctx, it.ID, atraso); err != nil {
		d.log.Error("handing back to the queue", "run", it.RunID, "error", err)
	}
}

// backoff is how long the run waits before its next attempt.
//
// It DOUBLES until it reaches the cap, rather than computing
// `base << (attempt-1)` and clamping afterwards. That shift is where the
// arithmetic goes wrong, and it goes wrong quietly: at attempt 35 with a
// one-second base the product overflows int64 and comes back NEGATIVE, at
// attempt 63 it comes back ZERO -- and a zero delay is an instant requeue, a
// hot loop against whatever was already failing. Whether an overflow lands on
// a plausible-looking small POSITIVE number depends on the base, which is to
// say on a flag somebody set.
//
// A loop that returns the moment it passes the cap cannot overflow at all: the
// value never grows past BackoffMax + one doubling. The attempt count is a
// handful, so the cost is nothing.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	delay := d.cfg.BackoffBase
	if delay <= 0 || delay >= d.cfg.BackoffMax {
		return d.cfg.BackoffMax
	}
	for a := 1; a < attempt; a++ {
		delay *= 2
		if delay >= d.cfg.BackoffMax {
			return d.cfg.BackoffMax
		}
	}
	return delay
}

// window returns the schedule's interval for a slot, or nothing.
//
// It reads the run's OWN definition -- the snapshot taken at the trigger -- and
// not the workflow as it is published now. A backfill of a slot from March has
// to produce the window March had, and a schedule edited since must not rewrite
// what an old run covers.
func (d *Dispatcher) window(ctx context.Context, runID uuid.UUID) func(time.Time) (time.Time, time.Time) {
	r, err := d.repo.Get(ctx, runID)
	if err != nil {
		return nil
	}
	var def struct{ Schedule, Timezone string }
	if json.Unmarshal(r.Definition, &def) != nil || def.Schedule == "" {
		return nil
	}
	return func(slot time.Time) (time.Time, time.Time) {
		start, end, err := sch.Schedule{Cron: def.Schedule, Timezone: def.Timezone}.Window(slot)
		if err != nil {
			return time.Time{}, time.Time{}
		}
		return start, end
	}
}

// measure records a run that reached a TERMINAL state.
//
// Only terminal, and that is the whole reason it is a separate function rather
// than a line in process(): a run that fails and retries passes through
// process() three times, and counting each pass would report three runs where
// the operator saw one. The per-attempt number already exists -- it is the step
// duration, recorded by the runner.
//
// The duration runs from the run's CREATION, not from the claim: "how long did
// my pipeline take" includes the wait, and a number that excludes queue time
// looks healthy exactly when the queue is the problem.
//
// The read is one query per terminal run, and it buys the workflow and trigger
// labels. Without them the counter answers "how many runs failed" and never
// "which pipeline" -- which is the only version of the question anybody asks.
func (d *Dispatcher) measure(ctx context.Context, runID uuid.UUID, status dom.Status) {
	if d.Metrics == nil {
		return
	}
	r, err := d.repo.Get(ctx, runID)
	if err != nil {
		// Labelled rather than dropped. Dropping keeps the labels clean and
		// makes brevis_run_total quietly wrong; "unknown" keeps the total
		// honest and announces itself on the chart.
		d.log.Warn("run measured without its workflow", "run", runID, "error", err)
		d.Metrics.RunFinished(ctx, "unknown", string(status), "unknown", 0)
		return
	}
	d.Metrics.RunFinished(ctx, r.WorkflowSlug, string(status), r.TriggerType,
		time.Since(r.CreatedAt))
}

// raise returns the builder that turns this failure into outbox rows.
//
// It BUILDS and does not send, which is the whole change. Nothing here can fail
// in a way that matters: a Slack outage used to be a log line and a lost alert,
// and is now a row somebody retries.
//
// Nil when no channel is configured. An outbox filling with alerts nothing can
// deliver would be worse than not writing them -- it would look like a backlog
// instead of like a setting nobody turned on.
//
// The returned closure runs INSIDE the attempt's transaction, so what it writes
// commits with the attempt that justified it or not at all. It is called on
// every attempt, because a step may declare `when: attempt`; `gaveUp` is what
// the run-level alert waits for.
func (d *Dispatcher) raise(ctx context.Context, runID uuid.UUID, cause error) func(int, bool) []alerts.Pending {
	if d.Channel == "" {
		return nil
	}
	return func(attempt int, gaveUp bool) []alerts.Pending {
		// The details come from the database: the dispatcher knows only the id.
		// If the read fails the alert still goes out -- half a message beats
		// none when something is already broken.
		base := notify.Alert{
			RunID: runID.String(), Status: string(dom.StatusFailed),
			Attempts: attempt, Err: cause.Error(), BaseURL: d.BaseURL,
		}
		var w wfdom.Workflow
		if r, err := d.repo.Get(ctx, runID); err == nil {
			base.Workflow, base.Trigger, base.LogicalDate = r.WorkflowSlug, r.TriggerType, r.LogicalDate
			var def struct{ Tags []string }
			if json.Unmarshal(r.Definition, &def) == nil {
				base.Tags = def.Tags
			}
			// The run's SNAPSHOT, not the published workflow: a file edited
			// since the trigger must not change who gets told about a run that
			// used the previous definition.
			if err := json.Unmarshal(r.Definition, &w); err != nil {
				d.log.Warn("cannot read the run's steps for on_error", "run", runID, "error", err)
			}
		} else {
			d.log.Warn("alert without the run's details", "run", runID, "error", err)
		}

		var out []alerts.Pending

		// The run-level alert: only when the run gives up. Warning on every
		// attempt would turn a run that recovers on its second try into two
		// alerts and a silence, and a channel that cries wolf stops being read.
		if gaveUp {
			a := base
			if step, log, err := d.repo.FailedStep(ctx, runID); err == nil {
				a.Step = step
				a.LogExcerpt = lastLines(log, 15)
			} else {
				d.log.Warn("alert without the step that failed", "run", runID, "error", err)
			}
			out = append(out, alerts.Pending{
				RunID: runID, Kind: alerts.KindRun, Channel: d.Channel, Payload: a,
			})
		}

		out = append(out, d.stepAlerts(ctx, runID, w, base, gaveUp)...)
		return out
	}
}

// stepAlerts builds one alert per failed step that asked for one.
//
// The channel comes from the INSTALLATION and not from the step's declaration:
// on_error.type names a kind of destination, and where that destination is is a
// credential the workflow's author does not necessarily get to choose. A step
// that names a channel the installation has not configured is dropped here with
// a log line, rather than written and given up on later -- the outbox is for
// alerts that could have arrived.
func (d *Dispatcher) stepAlerts(ctx context.Context, runID uuid.UUID,
	w wfdom.Workflow, base notify.Alert, gaveUp bool,
) []alerts.Pending {
	declared := map[string]*wfdom.OnError{}
	for _, n := range w.Nodes {
		if n.OnError.Fires(gaveUp) {
			declared[n.ID] = n.OnError
		}
	}
	if len(declared) == 0 {
		// The common case, and it costs no query: most steps declare nothing,
		// and the run-level alert already covers the run.
		return nil
	}

	failed, err := d.repo.FailedSteps(ctx, runID)
	if err != nil {
		d.log.Warn("cannot read which steps failed for on_error", "run", runID, "error", err)
		return nil
	}

	var out []alerts.Pending
	for node, on := range declared {
		log, itFailed := failed[node]
		if !itFailed {
			// A step that did not fail is not announced, however the run ended.
			// A workflow where one branch fails must not wake up whoever owns
			// the other one.
			continue
		}
		if on.Type != d.Channel {
			d.log.Warn("step declares a channel this installation has not configured",
				"run", runID, "step", node, "declared", on.Type, "configured", d.Channel)
			continue
		}
		a := base
		a.Step = node
		a.LogExcerpt = lastLines(log, 15)
		out = append(out, alerts.Pending{
			RunID: runID, Kind: alerts.KindStep, NodeID: node,
			Channel: d.Channel, Payload: a,
		})
	}
	return out
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
