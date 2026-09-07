// Package execution (application) walks the graph and runs its nodes.
//
// This is the LOCAL version: no queue, no persistence, no scheduler -- those
// pieces have phases of their own in the plan (§37, phases 2 and 4). What lives
// here is enough for `brevis run file.yaml` to run on the instance itself,
// which is what was asked for.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/graph"
	"github.com/AreteAcademy/brevis/internal/observability/metrics"
	"time"
)

// Reporter receives the execution's events. A small interface so the CLI, the
// tests and the persister can all watch the same stream.
type Reporter interface {
	Evento(execution.Event)
}

// Persistidor records each step's state. Optional: the local `brevis run` has
// no database, and requiring one would make an ad-hoc execution depend on
// infrastructure.
// Historico answers whether a step has ever succeeded. It is what decides
// whether this is its FIRST run -- something the step does not know and only
// the engine holds.
//
// A small interface, declared here in the consumer rather than in the package
// that implements it.
//
// The question is per (workflow, step), not per workflow: a workflow with three
// fetchers writing to three tables would create only the first step's if the
// answer covered the whole workflow, and the other two would fail in silence.
type History interface {
	StepHasSucceeded(ctx context.Context, workflowSlug, nodeID string, exceto uuid.UUID) (bool, error)

	// AlreadySucceeded returns the INSTANCES of THIS run that finished well in
	// an earlier attempt of it, so a retry re-runs only what failed.
	//
	// Keyed by instance and not by node, because a mapped step's twenty
	// partitions do not fail together: a retry has to redo the two that broke
	// and leave the other eighteen alone.
	//
	// A different question from StepHasSucceeded, which asks about EARLIER
	// RUNS. Confusing the two would make a step skip itself forever after its
	// first good day, so they are separate methods rather than one with a
	// flag.
	AlreadySucceeded(ctx context.Context, runID uuid.UUID) (map[run.StepKey]bool, error)
}

// The step is identified by a run.StepKey and not by a bare node id, because a
// mapped step (`for_each:`) has one row per instance. It is a struct rather
// than a fifth positional int next to `attempt`, which is how the wrong one
// gets passed.
type Persister interface {
	IniciarTask(ctx context.Context, runID uuid.UUID, step run.StepKey, attempt int) error
	TerminarTask(ctx context.Context, runID uuid.UUID, step run.StepKey, attempt int,
		status run.Status, exit *int, failure string, log string) error

	// RecordStages records the phases of an SDK step while it runs. It is
	// what makes the screen advance before the step finishes.
	RecordStages(ctx context.Context, runID uuid.UUID, step run.StepKey, attempt int,
		sdkVersion string, stages json.RawMessage) error

	// MarkSkipped records a step whose trigger rule was not satisfied, with the
	// reason. Required rather than optional: a skipped step that leaves no row
	// is invisible, and "did not run and nobody can tell why" is the state this
	// whole feature exists to remove.
	MarkSkipped(ctx context.Context, runID uuid.UUID, step run.StepKey, attempt int,
		reason string) error
}

// Runner runs a whole workflow.
//
// It holds TWO executors and picks per node: `run:` goes to the process one,
// `action:` resolves in the Go registry. The choice belongs to the runner and
// not to the executor, so each executor can go on ignoring that the other
// exists.
type Runner struct {
	Processo execution.Executor // serves `run:`; may be nil when there are only Go tasks
	Go       execution.Executor // serves `action:`; may be nil

	WorkDir string
	Env     map[string]string
	Report  Reporter

	// Timeout per node. Zero means no limit.
	Timeout time.Duration

	// MaxAttempts per node. Zero or 1 means a single attempt.
	MaxAttempts int
	BackoffBase time.Duration

	// Persist and RunID are used together: without both, per-step state is not
	// recorded and the DAG in the UI shows up with no execution state.
	Persist Persister

	// ContextDir is where a step's published context is written on the ENGINE's
	// filesystem, for the executors that read a real file.
	//
	// Empty is the normal case and does NOT turn the feature off: Run creates a
	// temporary directory per run and removes it at the end. It was "empty
	// means off" for one commit, and the consequence was that the whole feature
	// worked in tests and did nothing for a user, because nothing outside a
	// test ever set it. A capability that has to be switched on by a field
	// nobody knows about is a capability nobody has.
	//
	// Set it to pin the location -- a test that wants to read the files back.
	ContextDir string

	// published is what each step of this run has published so far, keyed by
	// step id. A mutex because parallel steps write it at the same time, and a
	// pointer-free map on the Runner would be copied per node.
	published *publishedContext
	RunID     uuid.UUID

	// Params are this run's values. They reach the step's command through a
	// template (see execution.Render) and the step's environment, so a
	// fetcher using the SDK sees them without being handed an argument.
	Params map[string]string

	// Trigger says why this Run exists: schedule, manual or backfill.
	Trigger string

	// LogicalDate is the slot this Run stands for. Nil on a manual trigger.
	LogicalDate *time.Time

	// Historico decides whether a step is running for the first time. Nil means
	// there is no way to know -- and then the step gets first=false, because
	// creating a table without being sure is worse than not creating it.
	History History

	// Vagas caps how many STEPS run at once -- in Kubernetes, how many pods
	// exist simultaneously. Nil means no limit.
	//
	// It has to be shared across every Runner in the process, which is why it
	// is injected rather than created here: the ceiling belongs to the CLUSTER,
	// not to one workflow. Without it, the dispatcher's concurrency limit
	// counted RUNS -- five runs with three parallel steps each gave fifteen
	// pods, not five.
	Slots chan struct{}

	// TentativaDoRun is this RUN's attempt, counted by the dispatcher. It goes
	// into the pod name so a retry does not find the previous attempt's pod.
	RunAttempt int

	// Pods runs steps as pods in Kubernetes. When present it serves every step
	// that declares `image:` -- and the same DAG runs as a pod in the cluster
	// and as a process on a laptop, with no change to the YAML.
	Pods execution.Executor

	// Metrics records per-step numbers. Nil means nothing is measured, which is
	// what `brevis run` on a laptop wants: it has no endpoint to scrape.
	//
	// The STEP is measured here and the RUN is measured by the dispatcher,
	// which is the only place that knows a failure was the last attempt rather
	// than one of three.
	Metrics *metrics.Metrics
}

// Run walks the graph by levels: everything inside a level runs in parallel,
// and the next level only starts once the previous one closes entirely.
//
// It walks the WHOLE graph, and a failure does not stop it. What a failure
// stops is the branch below it: every step whose dependencies did not all
// succeed is marked `skipped`, with the step responsible named. A branch that
// does not touch the failure keeps going -- Airflow's rule, and what anybody
// arriving from it expects.
//
// It used to abort at the first failure, and the reason was a real one:
// carrying on after an error produced a partial result that looked complete,
// and a pipeline ran 28 days late without anyone seeing it. What replaces that
// protection is not nothing. The run still FAILS, with the same error; the
// failed step is red and the skipped ones are their own colour; and the alert
// still goes out. A partial result no longer looks complete because the run
// says it is not.
//
// The cost is real and worth naming: more steps run on a failing run. What it
// does NOT cost is paying for them twice -- a retry re-runs only what failed;
// see alreadySucceeded.
func (r Runner) Run(ctx context.Context, w wf.Workflow) error {
	levels, err := graph.Levels(w)
	if err != nil {
		return err
	}

	// One per Run, seeded from what a resumed run already has.
	//
	// It is created here and not on the struct because Runner travels by value:
	// every node gets a copy, and only a pointer makes what one step published
	// visible to the next.
	if r.published == nil {
		r.published = newPublished(r.seedContext(ctx))
	}

	// And somewhere for the steps to write. Created here, once, because
	// building a task must not do I/O -- and defaulted rather than required,
	// so the feature is on for everybody instead of on for whoever knew to set
	// the field.
	if r.ContextDir == "" {
		dir, err := os.MkdirTemp("", "brevis-context-*")
		if err != nil {
			// Not fatal: losing the context between steps is worse than
			// nothing, but losing the RUN over a temp directory is worse
			// still. The steps that read will report the missing key
			// themselves, which names the step and the key.
			slog.WarnContext(ctx, "context between steps is off for this run",
				"reason", "could not create a temporary directory", "error", err)
		} else {
			r.ContextDir = dir
			defer func() { _ = os.RemoveAll(dir) }()
		}
	}
	porID := make(map[string]wf.Node, len(w.Nodes))
	for _, n := range w.Nodes {
		porID[n.ID] = n
	}
	// Built once. It is the same map runcontext.Visible wants, and a trigger
	// rule asks the same question a context lookup does: what does this step
	// depend on.
	deps := upstream(w)

	// What an earlier attempt of THIS run already got right. A retry re-runs
	// only what failed, the way a cleared DAG run does in Airflow.
	//
	// Read once, before anything starts: reading it per step would let two
	// attempts of the same run disagree with each other halfway through.
	settled := r.alreadySucceeded(ctx)

	// What each step ended as, so the level below can read its trigger rule
	// against something real.
	done := &outcomes{by: map[string]run.Status{}}

	// The FIRST failure, kept and returned at the end. It used to be returned
	// immediately, which is why nothing below a failure was ever recorded: the
	// steps that would not run simply had no row, and the screen showed them
	// pending forever.
	//
	// The run's OUTCOME is unchanged -- it still fails, with the same error.
	// What changed is that the graph now says which steps were skipped and
	// why.
	var firstFailure error

	for i, level := range levels {
		if ctx.Err() != nil {
			// Cancelled. Marking the rest skipped would record a decision that
			// was never made: those steps were not ruled out, the run was
			// stopped.
			break
		}
		if err := r.runLevel(ctx, w, level, porID, deps, done, settled); err != nil && firstFailure == nil {
			firstFailure = fmt.Errorf("nivel %d: %w", i+1, err)
		}
	}
	return firstFailure
}

// outcomes is what each step ended as. A mutex because a level runs in
// parallel.
type outcomes struct {
	mu sync.Mutex
	by map[string]run.Status
}

func (o *outcomes) set(id string, s run.Status) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.by[id] = s
}

func (o *outcomes) get(id string) run.Status {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s, ok := o.by[id]; ok {
		return s
	}
	return run.StatusPending
}

func (r Runner) runLevel(ctx context.Context, w wf.Workflow, level []string,
	porID map[string]wf.Node, deps map[string][]string, done *outcomes,
	settled map[run.StepKey]bool,
) error {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		errs   []error
		record []func()
	)

	for _, id := range level {
		n := porID[id]

		// Already done, in an earlier attempt of this same run. It is NOT
		// marked skipped: it is a success, its row says so, and overwriting
		// that row would erase the only record of the work actually happening.
		//
		// Checked before the trigger rule on purpose. A step that already
		// succeeded is settled, and re-deciding it against the new attempt's
		// outcomes could rule out something that has already run -- which
		// would be the engine changing its mind about the past.
		// The rule is read BEFORE anything in this level starts, so every step
		// in it sees the same picture. Deciding inside the goroutine would let
		// two siblings disagree about whether something had failed, depending
		// on which one the scheduler woke first.
		if why := r.notEligible(n, deps[n.ID], done); why != "" {
			r.markSkipped(ctx, run.Step(n.ID), why)
			done.set(n.ID, run.StatusSkipped)
			continue
		}

		// And then the data. It comes after the trigger rule because the rule
		// is about whether the step's turn arrived at all, and this is about
		// whether there is anything for it to do -- asking the second first
		// would read a key from a step that never ran.
		if why, err := r.gate(n); err != nil {
			// A key the publisher never published is a FAILURE, not an empty
			// value. Treating a typo as "empty" disables the step silently and
			// forever, and a nightly that stops running with nothing anywhere
			// saying why is the worst outcome this feature can have.
			r.fail(ctx, run.Step(n.ID), err, &mu, &errs, done)
			continue
		} else if why != "" {
			r.markSkipped(ctx, run.Step(n.ID), why)
			done.set(n.ID, run.StatusSkipped)
			continue
		}

		// And now HOW MANY times it runs. An unmapped step is one instance; a
		// `for_each:` step is one per element of the list, each with a row and
		// a retry of its own.
		instances, why, err := r.expand(n)
		if err != nil {
			r.fail(ctx, run.Step(n.ID), err, &mu, &errs, done)
			continue
		}
		if why != "" {
			// An empty list is SKIPPED, not success. A step that did nothing
			// because there was nothing to do did not succeed at doing it, and
			// a green node over zero instances is the kind of thing somebody
			// builds a dashboard on.
			r.markSkipped(ctx, run.Step(n.ID), why)
			done.set(n.ID, run.StatusSkipped)
			continue
		}

		// One outcome for the node, made of its instances. `failed` wins:
		// three partitions loading and one not is a step that did not do its
		// job, and the steps below it must see that.
		var (
			perNode sync.Mutex
			broke   bool
		)
		for _, inst := range instances {
			if settled[inst.key] {
				// Already done on an earlier attempt of this same run. It is
				// NOT marked skipped: it is a success, its row says so, and
				// overwriting that row would erase the only record of the work
				// happening.
				//
				// Per INSTANCE, so a retry of a mapped step redoes the two
				// partitions that failed and leaves the other eighteen alone.
				if r.Report != nil {
					r.Report.Evento(execution.Event{
						Kind: execution.EventLog, NodeID: n.ID, Stream: "stdout",
						Message: "already succeeded on an earlier attempt of this run; not re-run",
					})
				}
				continue
			}

			wg.Add(1)
			go func(n wf.Node, inst instance) {
				defer wg.Done()
				if err := r.runNode(ctx, w, n, inst); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					perNode.Lock()
					broke = true
					perNode.Unlock()
				}
			}(n, inst)
		}
		// The node's own outcome is settled after the level's wait below, so
		// it is recorded through a closure the wait runs.
		record = append(record, func() {
			perNode.Lock()
			defer perNode.Unlock()
			if broke {
				done.set(n.ID, run.StatusFailed)
				return
			}
			done.set(n.ID, run.StatusSuccess)
		})
	}
	wg.Wait()
	for _, settle := range record {
		settle()
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// fail records a step that could not even start: a gate that could not be read,
// a list that is not a list. It goes through markStart and markEnd so it lands
// on the screen as a step that failed with a message, which is the shape the
// run's page already knows how to show.
func (r Runner) fail(ctx context.Context, step run.StepKey, cause error,
	mu *sync.Mutex, errs *[]error, done *outcomes,
) {
	r.markStart(ctx, step, 0)
	r.markEnd(ctx, step, 0, cause, "")
	mu.Lock()
	*errs = append(*errs, cause)
	mu.Unlock()
	done.set(step.Node, run.StatusFailed)
}

// instance is one run of a step: the key that identifies its row, and the
// element it was mapped over.
type instance struct {
	key run.StepKey

	// value is the element, as it was published. Empty when the step is not
	// mapped, which is most of them.
	value json.RawMessage
}

// expand turns a step into the instances that will run it.
//
// Three outcomes, the same shape as gate():
//
//   - instances, "", nil -- run these.
//   - nil, reason, nil   -- skip it: the list is empty.
//   - nil, "", err       -- fail it: the key is missing or is not a list.
//
// The DAG's shape does not change here, and that is what makes this cheap: a
// mapped step is still ONE node with one set of edges, and only the number of
// rows under it varies. graph.Levels never sees it.
func (r Runner) expand(n wf.Node) ([]instance, string, error) {
	if n.ForEach == "" {
		return []instance{{key: run.Step(n.ID)}}, "", nil
	}
	step, key, _ := strings.Cut(n.ForEach, ".")

	raw, published := r.published.snapshot()[step]
	if !published {
		return nil, "", fmt.Errorf("step %q maps over `%s`, and %q published nothing",
			n.ID, n.ForEach, step)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, "", fmt.Errorf("step %q maps over `%s`, and %q published something "+
			"that is not an object: %w", n.ID, n.ForEach, step, err)
	}
	v, ok := values[key]
	if !ok {
		return nil, "", fmt.Errorf("step %q maps over `%s`, and %q published %s but not %q",
			n.ID, n.ForEach, step, quotedRawKeys(values), key)
	}

	var list []json.RawMessage
	if err := json.Unmarshal(v, &list); err != nil {
		// A scalar is not a list of one. Guessing would make `for_each: x`
		// behave differently depending on how many elements happened to be
		// there today, which is the kind of rule nobody can hold in their head.
		return nil, "", fmt.Errorf("step %q maps over `%s`, which is not a list", n.ID, n.ForEach)
	}
	if len(list) == 0 {
		return nil, "`" + n.ForEach + "` is an empty list", nil
	}

	out := make([]instance, 0, len(list))
	for i, item := range list {
		out = append(out, instance{key: run.StepKey{Node: n.ID, MapIndex: i}, value: item})
	}
	return out, "", nil
}

func quotedRawKeys(values map[string]json.RawMessage) string {
	if len(values) == 0 {
		return "nothing"
	}
	out := make([]string, 0, len(values))
	for k := range values {
		out = append(out, strconv.Quote(k))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// notEligible returns WHY a step does not run, or "" when it does.
//
// A reason rather than a bool: it is written into the step's row and shown on
// the screen, and "skipped" with no explanation sends whoever is looking at the
// graph to trace edges by hand.
func (r Runner) notEligible(n wf.Node, upstream []string, done *outcomes) string {
	switch n.WhenOf() {
	case wf.WhenAnyFailed:
		for _, up := range upstream {
			if done.get(up) == run.StatusFailed {
				return ""
			}
		}
		if len(upstream) == 0 {
			// A step with no dependencies can never see one fail. Refusing it
			// at publish would be the better answer; until then it is skipped
			// with a message that says so rather than running every time.
			return "`when: any_failed` on a step with no depends_on: nothing it waits for can fail"
		}
		return "no step it depends on failed"

	case wf.WhenAllDone:
		for _, up := range upstream {
			// TerminalStep, not Terminal: a run's failure can become a retry,
			// a step's cannot. Asking the run's question here made `all_done`
			// wait forever on a step that had already failed.
			if !done.get(up).TerminalStep() {
				return "`" + up + "` has not finished"
			}
		}
		return ""

	default: // WhenAllSuccess
		// LOCAL: this step's own dependencies, and nothing else. A failure
		// somewhere unrelated does not reach here, so a healthy branch keeps
		// going -- which is what Airflow does and what anybody arriving from it
		// expects. See wf.WhenAllSuccess for what that gave up.
		for _, up := range upstream {
			if s := done.get(up); s != run.StatusSuccess {
				return "`" + up + "` was " + string(s)
			}
		}
		return ""
	}
}

// gate reads `unless_empty:` and says whether there is anything to do.
//
// Three outcomes, and the middle one is the reason it returns two values:
//
//   - "", nil     -- run it. Either no gate, or the value is present.
//   - reason, nil -- skip it: the value is there and it is empty.
//   - "", err     -- FAIL it: the key is not there at all.
//
// The last one is a decision. A missing key could be read as "empty, so skip",
// and that is what a forgiving engine would do -- and a typo in the key name
// would then disable the step silently, forever, with nothing anywhere saying
// why. Failing names the key and lists what the step actually published.
func (r Runner) gate(n wf.Node) (string, error) {
	if n.UnlessEmpty == "" {
		return "", nil
	}
	step, key, _ := strings.Cut(n.UnlessEmpty, ".")

	raw, published := r.published.snapshot()[step]
	if !published {
		// Publish validates that `step` is visible, so reaching here means it
		// ran and wrote nothing at all.
		return "", fmt.Errorf("step %q reads `%s`, and %q published nothing",
			n.ID, n.UnlessEmpty, step)
	}

	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return "", fmt.Errorf("step %q reads `%s`, and %q published something "+
			"that is not an object: %w", n.ID, n.UnlessEmpty, step, err)
	}
	v, ok := values[key]
	if !ok {
		return "", fmt.Errorf("step %q reads `%s`, and %q published %s but not %q",
			n.ID, n.UnlessEmpty, step, quotedKeys(values), key)
	}
	if wf.IsEmpty(v) {
		return "`" + n.UnlessEmpty + "` is empty", nil
	}
	return "", nil
}

// quotedKeys lists what a step DID publish, so a typo is one line away from
// being obvious instead of one investigation.
func quotedKeys(values map[string]any) string {
	if len(values) == 0 {
		return "nothing"
	}
	out := make([]string, 0, len(values))
	for k := range values {
		out = append(out, strconv.Quote(k))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// alreadySucceeded asks the history what this run has already got right.
//
// A failure to read it is not a failure of the run: the answer degrades to
// "nothing succeeded yet", which re-runs the whole graph -- exactly what this
// engine did before, and a safe place to land.
func (r Runner) alreadySucceeded(ctx context.Context) map[run.StepKey]bool {
	if r.History == nil || r.RunID == uuid.Nil {
		return nil
	}
	settled, err := r.History.AlreadySucceeded(ctx, r.RunID)
	if err != nil {
		slog.WarnContext(ctx, "could not read what this run already finished; re-running everything",
			"run", r.RunID, "error", err)
		return nil
	}
	return settled
}

// markSkipped records the decision. Same rules as markStart: it needs both a
// persister and a RunID, and a write failure does not interrupt the run.
func (r Runner) markSkipped(ctx context.Context, step run.StepKey, reason string) {
	if r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: step.Node, Stream: "stderr",
			Message: "skipped: " + reason,
		})
	}
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	if err := r.Persist.MarkSkipped(ctx, r.RunID, step, 0, reason); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: step.Node, Stream: "stderr",
			Message: "could not record the skip: " + err.Error(),
		})
	}
}

// runNode runs one node, with retry.
//
// The retry is PER NODE, and not only per Run as in the dispatcher: redoing the
// whole workflow because a `notify.sh` failed would throw away the work already
// finished.
func (r Runner) runNode(ctx context.Context, w wf.Workflow, n wf.Node, inst instance) error {
	attempts := r.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var last error
	for t := 1; t <= attempts; t++ {
		// The slot is taken per ATTEMPT, not for the whole step: holding it
		// through the backoff would leave a cluster slot idle waiting on a
		// clock.
		libera, err := r.ocupar(ctx)
		if err != nil {
			return err
		}
		r.markStart(ctx, inst.key, t-1)
		// Counted per ATTEMPT and not per step: flapping -- a step that passes
		// on the third try, every night -- is invisible in a duration and
		// invisible on the screen, and this is the only number that shows it.
		r.Metrics.StepStarted(ctx, w.Slug, n.ID)

		started := time.Now()
		var outgoing string
		outgoing, last = r.tentar(ctx, w, n, inst, t-1)
		r.Metrics.StepFinished(ctx, w.Slug, n.ID, stepStatus(last), time.Since(started))

		r.markEnd(ctx, inst.key, t-1, last, outgoing)
		libera()
		if last == nil {
			return nil
		}
		if t == attempts {
			break
		}
		// Does not insist once the context is gone: that would be a retry against a cancellation.
		if ctx.Err() != nil {
			break
		}

		espera := r.BackoffBase * time.Duration(1<<uint(t-1))
		if r.Report != nil {
			r.Report.Evento(execution.Event{
				Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
				Message: fmt.Sprintf("attempt %d/%d failed, retrying in %s", t, attempts, espera),
			})
		}
		select {
		case <-time.After(espera):
		case <-ctx.Done():
			return last
		}
	}
	return last
}

// stepStatus is the label, and it says "failed" for every failed ATTEMPT --
// including one that a later attempt makes good. The run-level counter is where
// a retried-then-succeeded run appears once, as a success; conflating the two
// here would hide exactly the flapping the attempt counter exists to show.
func stepStatus(err error) string {
	if err == nil {
		return "success"
	}
	return "failed"
}

// ocupar takes a slot and returns the function that frees it.
//
// It blocks until there is room, and that is the asked-for behaviour: with ten
// ready steps and five slots, five run and the rest wait, entering as slots
// open. Refusing instead of waiting would turn an excess of work into a
// failure, when it is only a queue.
func (r Runner) ocupar(ctx context.Context) (func(), error) {
	if r.Slots == nil {
		return func() {}, nil
	}
	select {
	case r.Slots <- struct{}{}:
		var uma sync.Once
		return func() { uma.Do(func() { <-r.Slots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// markStart and markEnd only record when there is both a persister AND a
// RunID. A write failure does not interrupt the run: losing a step's record is
// bad, but aborting the workflow over it is worse.
func (r Runner) markStart(ctx context.Context, step run.StepKey, attempt int) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	if err := r.Persist.IniciarTask(ctx, r.RunID, step, attempt); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: step.Node, Stream: "stderr",
			Message: "could not record the start of the step: " + err.Error(),
		})
	}
}

// markStages records the phases as they advance. Failing here does NOT bring
// the step down: the screen is informative, and the truth about a step remains
// its exit code. Trading a run for a screen update would be the wrong bargain.
//
// It is only called after a marker has been recognised, which is what keeps an
// ordinary step from paying a database round trip per log line. Checking again
// here would be a verification that cannot fail.
func (r Runner) markStages(ctx context.Context, step run.StepKey, attempt int, c *stageCollector) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	data, err := json.Marshal(c.Stages)
	if err != nil {
		return
	}
	_ = r.Persist.RecordStages(ctx, r.RunID, step, attempt, c.Version, data)
}

func (r Runner) markEnd(ctx context.Context, step run.StepKey, attempt int, cause error, log string) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	status, msg := run.StatusSuccess, ""
	var exit *int
	if cause != nil {
		status, msg = run.StatusFailed, cause.Error()
		var step *StepError
		// Exit 0 is not recorded: a Go task that fails has no process, and a
		// zero in that column would read as "finished fine" next to a failed
		// status.
		if errors.As(cause, &step) && step.ExitCode != 0 {
			exit = &step.ExitCode
		}
	}
	if err := r.Persist.TerminarTask(ctx, r.RunID, step, attempt, status, exit, msg, log); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: step.Node, Stream: "stderr",
			Message: "could not record the end of the step: " + err.Error(),
		})
	}
}

// StepError is a step's failure, with the context needed to understand it
// without opening a log: the exit code, what it means, and the last lines the
// process wrote to stderr.
//
// Before, all that survived was "exited with code 127" -- technically correct
// and useless. The cause (`/bin/sh: python: not found`) went through the events
// as a log line and was dropped right there, so the screen showed the symptom
// without the explanation.
type StepError struct {
	NodeID   string
	ExitCode int
	Message  string

	// Saida is the last few lines of stderr. Only the last ones, and not all of
	// them, because a chatty process would fill the database's error column --
	// and the cause is almost always at the end.
	Output []string
}

func (e *StepError) Error() string {
	header := fmt.Sprintf("step %q: %s", e.NodeID, e.Message)
	if hint := codeHint(e.ExitCode); hint != "" {
		header += " (" + hint + ")"
	}
	if len(e.Output) == 0 {
		return header
	}
	return header + "\n" + strings.Join(e.Output, "\n")
}

// codeHint translates the exit codes the shell reserves. They are the most
// confusing ones: 127 is not an application error but a missing command -- the
// difference between looking for a defect in the code and looking in the
// image.
func codeHint(c int) string {
	switch c {
	case 126:
		return "the command is not executable"
	case 127:
		return "command not found -- check that it exists in the worker's image"
	case 130:
		return "interrompido por SIGINT"
	case 137:
		return "killed by SIGKILL -- usually out of memory"
	case 143:
		return "encerrado por SIGTERM"
	case -1:
		return "ended by a signal, with no exit code"
	}
	return ""
}

// contextLines is how many lines of stderr travel with a failure. Five
// cover a short stack trace or a command's closing message without drowning the
// screen.
const contextLines = 5

// tentar runs the step once and returns the complete output (capped) along with
// the outcome. The output comes back on success too: a step that finished fine
// but produced very little is a signal, and it is only visible in the log.
func (r Runner) tentar(ctx context.Context, w wf.Workflow, n wf.Node, inst instance, attempt int) (string, error) {
	// A marker does nothing and succeeds. Short-circuited HERE rather than
	// given an executor that runs `true`: an empty pod on a cluster costs a
	// scheduling round trip and an image pull to accomplish nothing, and on a
	// laptop it costs a process. The point of the step is the shape of the
	// graph, not the work.
	//
	// It still goes through markStart and markEnd around this call, so it
	// appears on the screen as a step that ran -- which is what makes an `end`
	// that depends on everything answer "did the whole thing finish?".
	if n.Marker {
		return "", nil
	}

	// Asked once per attempt, and not inside montar, because building a task
	// must not do I/O. A retry of the same run does not reopen the first
	// execution: if attempt 1 wrote a row, StepHasSucceeded already answers
	// yes; if it failed, this is still the first, which is right.
	first := r.firstRun(ctx, w.Slug, n.ID)

	exec, tarefa, err := r.build(w, n, inst, attempt, first)
	if err != nil {
		return "", err
	}

	eventos, err := exec.Execute(ctx, tarefa)
	if err != nil {
		return "", fmt.Errorf("step %q: %w", n.ID, err)
	}

	var failure *StepError
	var stderr, stdout []string

	// The whole output (capped) goes to the database. The 5-line windows below
	// still exist for the error MESSAGE, which has to fit in a Slack alert;
	// this one keeps what the operator will want to read later, when the pod
	// that produced it is long gone.
	var completa window

	// The phases the step announces, when it is an SDK pipeline.
	var stages stageCollector

	for e := range eventos {
		// A marked line is the SDK talking to the engine, not the program's
		// output. It becomes a phase on the screen and does NOT enter the log
		// or the Report: whoever looks wants to see the phases, not the JSON
		// that carried them.
		if e.Kind == execution.EventLog {
			if line := strings.TrimSpace(e.Message); line != "" && stages.line(line) {
				r.markStages(ctx, inst.key, attempt, &stages)
				continue
			}
		}

		// What the step published. It never reaches the log: it is the step
		// talking to the steps below it, not to a person.
		if e.Kind == execution.EventContext {
			r.collectContext(ctx, n, inst, attempt, e.Message)
			continue
		}

		if r.Report != nil {
			r.Report.Evento(e)
		}
		// Keeps a sliding window of the last lines. It has to collect ALWAYS,
		// and not only after a failure: by the time the failure event arrives,
		// the lines that explain it have already gone by.
		//
		// The two streams, kept apart: not every program writes errors to
		// stderr. dbt prints "Parsing Error / Env var required but not
		// provided" on STDOUT, and capturing only stderr left the failure as
		// "exited with code 2", without the cause that was on screen the whole
		// time.
		if e.Kind == execution.EventLog {
			if line := strings.TrimSpace(e.Message); line != "" {
				completa.Write(line)
				target := &stdout
				if e.Stream == "stderr" {
					target = &stderr
				}
				*target = append(*target, line)
				if len(*target) > contextLines {
					*target = (*target)[1:]
				}
			}
		}
		if e.Kind == execution.EventFailed {
			failure = &StepError{NodeID: n.ID, ExitCode: e.ExitCode, Message: e.Message}
		}
	}
	if failure == nil {
		return completa.String(), nil
	}
	// stderr first: when it exists, it is where the program meant to report an
	// error. stdout only enters in its absence, so the message is not filled
	// with the ordinary output of a command that merely ended badly.
	failure.Output = stderr
	if len(failure.Output) == 0 {
		failure.Output = stdout
	}
	return completa.String(), failure
}

// Prefix of the variables that describe THIS run, kept apart from the
// BREVIS_SDK_ that configures the SDK: one says what the SDK does, the other
// what this particular dispatch is.
//
// This is not a private channel. The step's process can read its own
// environment, and somebody will. What is promised is that it does NOT HAVE TO
// -- not that it cannot.
const (
	envRunID      = "BREVIS_RUN_ID"
	envRunFirst   = "BREVIS_RUN_FIRST"
	envRunAttempt = "BREVIS_RUN_ATTEMPT"

	// A mapped step's instance. Absent on every unmapped step, which is most
	// of them: a variable that is always there and always empty teaches
	// whoever reads the environment to ignore it.
	envMapIndex       = "BREVIS_MAP_INDEX"
	envMapValue       = "BREVIS_MAP_VALUE"
	envRunTrigger     = "BREVIS_RUN_TRIGGER"
	envRunLogicalDate = "BREVIS_RUN_LOGICAL_DATE"
	envRunParams      = "BREVIS_RUN_PARAMS"
)

// seedContext loads what the steps of a RESUMED run already published.
//
// This is the case that forced the persistence: the engine skips a step that
// already succeeded, so the step below it would find nothing where the first
// attempt left something. Without this, resuming a run is not resuming it.
func (r Runner) seedContext(ctx context.Context) map[string]json.RawMessage {
	if r.History == nil || r.RunID == uuid.Nil {
		return nil
	}
	reader, ok := r.History.(ContextReader)
	if !ok {
		return nil
	}
	out, err := reader.PublishedContext(ctx, r.RunID)
	if err != nil {
		// A run that cannot read its own history still runs; the steps below
		// will report the missing key themselves, which is a better place to
		// find out than a failure before anything started.
		return nil
	}
	return out
}

// ContextReader is optional, for the same reason ContextPersister is: a History
// written before this feature still satisfies the runner.
type ContextReader interface {
	PublishedContext(ctx context.Context, runID uuid.UUID) (map[string]json.RawMessage, error)
}

// collectContext validates what a step published and keeps it.
//
// The validation is HERE and not in the executor because this is the side that
// knows somebody downstream was going to read it. A payload the platform
// truncated is reported as an error on the step's log, loudly, rather than
// dropped -- dropping it hands the next step a missing key with nothing
// anywhere saying why, which is the failure this whole feature is against.
func (r Runner) collectContext(ctx context.Context, n wf.Node, inst instance, attempt int, raw string) {
	parsed, err := runcontext.Parse([]byte(raw))
	if err != nil {
		if r.Report != nil {
			r.Report.Evento(execution.Event{
				Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
				Message: "brevis: the context this step published was not usable: " + err.Error(),
			})
		}
		return
	}
	if parsed == nil {
		return
	}

	// A MAPPED step's output is recorded on its row but does NOT become
	// context for the steps below.
	//
	// Four instances publishing under one step's name is four values for one
	// key, and there is no answer to `context.String("load.bucket")` that is
	// not a guess. Inventing a list shape here would change what that call
	// means for every step in the system to serve a case nobody has asked for
	// yet. It is said out loud rather than dropped quietly, because a step
	// whose publish silently goes nowhere is the worst way to find this out.
	if n.ForEach != "" {
		if r.Report != nil {
			r.Report.Evento(execution.Event{
				Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
				Message: "this step is mapped, so what it published is recorded on its " +
					"row but is not visible to the steps below: one key cannot hold " +
					"one value per instance",
			})
		}
		r.recordContext(ctx, inst, attempt, parsed)
		return
	}

	r.published.put(n.ID, parsed)
	r.recordContext(ctx, inst, attempt, parsed)
}

// recordContext writes what a step published onto its own row.
//
// Immediately, and not at the end of the step: a run that resumes reads this
// back, and the process that would write it later is exactly the one that may
// not survive.
func (r Runner) recordContext(ctx context.Context, inst instance, attempt int, parsed json.RawMessage) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	if p, ok := r.Persist.(ContextPersister); ok {
		_ = p.RecordContext(ctx, r.RunID, inst.key, attempt, parsed)
	}
}

// ContextPersister is optional, so a Persister written before this feature
// still satisfies the runner.
//
// The alternative -- adding the method to Persister -- would break every
// implementation at once, including the fakes in this package's own tests, for
// a capability most of them do not need.
type ContextPersister interface {
	RecordContext(ctx context.Context, runID uuid.UUID, step run.StepKey, attempt int,
		published json.RawMessage) error
}

// publishedContext holds what each step published, for the steps below it.
//
// It lives on the Runner and not in the database round trip because a run in
// flight has not written most of it yet: the step that just finished is the one
// the next step needs, and it is here before it is anywhere else.
//
// A resumed run seeds it from the history, which is the case that forced the
// persistence in the first place -- the skipped step's output still has to
// reach the step below it.
type publishedContext struct {
	mu sync.Mutex
	by map[string]json.RawMessage
}

func newPublished(seed map[string]json.RawMessage) *publishedContext {
	by := map[string]json.RawMessage{}
	for k, v := range seed {
		by[k] = v
	}
	return &publishedContext{by: by}
}

func (p *publishedContext) put(nodeID string, raw json.RawMessage) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.by[nodeID] = raw
}

func (p *publishedContext) snapshot() map[string]json.RawMessage {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]json.RawMessage, len(p.by))
	for k, v := range p.by {
		out[k] = v
	}
	return out
}

// upstream is the dependency graph, as runcontext.Visible wants it: for each
// node, the nodes it depends on.
func upstream(w wf.Workflow) map[string][]string {
	up := map[string][]string{}
	for _, e := range w.Edges {
		up[e.To] = append(up[e.To], e.From)
	}
	return up
}

// runContext builds what the engine knows about this run and the step does not.
//
// primeira is resolved beforehand, by the caller, because it needs a database
// round trip and building a task must not do I/O.
func (r Runner) runContext(inst instance, first bool, attempt int) map[string]string {
	env := map[string]string{}

	// What a mapped step's instance is FOR. Without these two the four
	// instances are four identical processes, which is four times the work and
	// none of the point.
	//
	// The value is the element exactly as it was published: a JSON string
	// arrives without its quotes, because `for_each: extract.partitions` over
	// ["2026-01", "2026-02"] should hand a shell `2026-01` and not `"2026-01"`.
	// Anything else -- an object, a number, a list -- arrives as its JSON.
	if inst.key.MapIndex != run.Unmapped {
		env[envMapIndex] = strconv.Itoa(inst.key.MapIndex)
		env[envMapValue] = elementValue(inst.value)
	}

	// With no RunID there is no managed run: this is the `brevis run` path,
	// which executes a YAML on the spot and belongs to no history.
	//
	// Injecting the zero UUID here would be worse than injecting nothing: the
	// SDK decides it is under the engine by the PRESENCE of the id, so a
	// fetcher run by hand would start logging "running under Brevis" with an
	// invented id. The params still go, because `--param` is precisely how
	// input is passed on this path.
	if r.RunID != uuid.Nil {
		env[envRunID] = r.RunID.String()
		env[envRunFirst] = strconv.FormatBool(first)
		// Starts at zero, like the task_runs.attempt column.
		env[envRunAttempt] = strconv.Itoa(attempt)
	}

	if r.Trigger != "" {
		env[envRunTrigger] = r.Trigger
	}
	if r.LogicalDate != nil {
		env[envRunLogicalDate] = r.LogicalDate.UTC().Format(time.RFC3339)
	}
	if len(r.Params) > 0 {
		// An impossible error: a map[string]string always serialises. Ignoring
		// it here beats returning an error no caller could act on.
		if b, err := json.Marshal(r.Params); err == nil {
			env[envRunParams] = string(b)
		}
	}

	return env
}

// mesclarEnv merges the runner's environment with this run's.
//
// The runner's wins a collision: if somebody set BREVIS_RUN_PARAMS in the
// configuration they meant to, and the engine does not overwrite explicit
// configuration.
// mesclarEnv merges the maps in increasing order of precedence, except the
// first: `base` (the engine's global environment) beats the run context, which
// is how it has always been, and whatever comes after beats `base`.
func mesclarEnv(base, run map[string]string, above ...map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(run))
	for k, v := range run {
		out[k] = v
	}
	for k, v := range base {
		out[k] = v
	}
	for _, m := range above {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// firstRun asks the history whether this step has ever succeeded.
//
// With no history configured the answer is "not the first": creating a table
// without being sure is worse than not creating it, and the consumer can always
// ask explicitly.
func (r Runner) firstRun(ctx context.Context, slug, nodeID string) bool {
	if r.History == nil {
		return false
	}
	jaTeve, err := r.History.StepHasSucceeded(ctx, slug, nodeID, r.RunID)
	if err != nil {
		// A failed query must not turn into a table created by mistake.
		return false
	}
	return !jaTeve
}

// montar escolhe o executor e monta a task.
// instanceSuffix is empty for an unmapped step, so every pod name and every
// execution id in the system is byte for byte what it was.
func instanceSuffix(k run.StepKey) string {
	if k.MapIndex == run.Unmapped {
		return ""
	}
	return "-" + strconv.Itoa(k.MapIndex)
}

// elementValue renders one element of a mapped list for the step's environment.
func elementValue(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		// A JSON string, without its quotes: `for_each` over ["2026-01"] hands
		// a shell 2026-01, which is what the command was written expecting.
		return text
	}
	return string(raw)
}

func (r Runner) build(w wf.Workflow, n wf.Node, inst instance, attempt int, first bool) (execution.Executor, execution.TaskExec, error) {
	image := w.ImageFor(n)
	resources := w.ResourcesFor(n)

	t := execution.TaskExec{
		// The instance is in the execution id, and therefore in the POD's
		// name. Without it, four instances of a mapped step ask the cluster for
		// four pods with one name: the executor ADOPTS an existing pod rather
		// than starting a second, so three of the four would attach to the
		// first one's logs and report its exit code as their own.
		ExecutionID: w.Slug + ":" + inst.key.Node + instanceSuffix(inst.key),
		NodeID:      n.ID,
		Workflow:    w.Slug,
		RunID:       r.RunID.String(),
		// The attempt goes into the pod's NAME. Without it, a retry finds the
		// previous attempt's pod again -- and because the executor adopts an
		// existing pod (so it does not start two identical ones when the
		// process dies midway), it stays stuck on the broken pod forever. That
		// is what happened in dev: a pod Pending on insufficient CPU was
		// re-adopted on every retry.
		Attempt:    attempt,
		Image:      image,
		Shell:      n.UsaShell(),
		CPU:        resources.CPU,
		Memoria:    resources.Memory,
		CPUMax:     resources.CPULimit,
		MemoriaMax: resources.MemoryLimit,
		WorkDir:    r.WorkDir,
		// Order, weakest to strongest: run context, the engine's global
		// environment, the workflow's `env:`, the step's `env:`. The step
		// beats the global on purpose -- the other way round, a variable
		// declared in the file would lose in silence to a BREVIS_TASK_ENV
		// somebody configured months ago.
		Env:     mesclarEnv(r.Env, r.runContext(inst, first, attempt), w.EnvDe(n)),
		Secrets: w.SecretsDe(n),
		Timeout: r.Timeout,
	}

	// What the steps above published, scoped to what this one depends on.
	//
	// Scoped and not everything: it keeps the variable small on a fifty-step
	// DAG, and it makes reading a step you do not depend on impossible rather
	// than a race against the scheduler.
	visible := runcontext.Visible(upstream(w), n.ID)
	if in, err := runcontext.Assemble(r.published.snapshot(), visible); err == nil && in != "" {
		t.Env[runcontext.EnvInput] = in
	}
	if r.ContextDir != "" {
		t.OutputPath = filepath.Join(r.ContextDir,
			fmt.Sprintf("%s-%d.json", strings.ReplaceAll(n.ID, "/", "_"), attempt))
		t.Env[runcontext.EnvOutput] = t.OutputPath
	}

	if n.Action != "" {
		if r.Go == nil {
			return nil, t, fmt.Errorf("step %q usa `action: %s`, mas nenhum executor Go foi configurado", n.ID, n.Action)
		}
		t.Action, t.With = n.Action, n.With
		return r.Go, t, nil
	}

	// The command is rendered HERE, when the task is built, and not at publish
	// time: the same workflow runs with different params on every dispatch, and
	// a command frozen in the database would lose that.
	command, err := execution.Render(n.Run, r.Params)
	if err != nil {
		return nil, t, fmt.Errorf("step %q: %w", n.ID, err)
	}
	t.Command = command

	// A step with `image:` runs as a POD when a pod executor exists. That is the
	// difference between local mode and the cluster, and it lives HERE, in one
	// place -- the YAML is identical in both, and neither executor knows the
	// other exists.
	if image != "" && r.Pods != nil {
		return r.Pods, t, nil
	}
	if r.Processo == nil {
		if image != "" {
			return nil, t, fmt.Errorf("step %q declares `image: %s`, but this process has "+
				"neither a pod executor nor a process executor", n.ID, image)
		}
		return nil, t, fmt.Errorf("step %q usa `run:`, mas nenhum executor de processo foi configurado", n.ID)
	}
	// Local with an `image:` declared: runs on the instance itself and WARNS.
	// Staying quiet would make it look as though the step ran in the declared
	// image, which is the kind of mistake that only surfaces once the result is
	// already wrong.
	if image != "" && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
			Message: fmt.Sprintf("modo local: rodando na instancia, ignorando `image: %s`", image),
		})
	}
	return r.Processo, t, nil
}
