package sdk

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Pipeline is a whole fetcher as a value. Run takes it from here: flags,
// -dry-run, logging and the exit code.
//
// See ExamplePipeline for a runnable one, compiled with the rest of the
// package so it stays true.
//
// Anything this does not cover is still reachable by calling Extract and Load
// directly.
type Pipeline struct {
	// Source is where records come from: the driver in From, plus the preview
	// and the counters that every origin honours.
	Source Source

	// Transform reshapes each record between Extract and Load, in order. See
	// Transformer.
	Transform []Transformer

	Target Target

	// Checkpoint keeps the raw extract so a second attempt of the same run does
	// not go back to the source. Zero turns it off. See Checkpoint.
	Checkpoint Checkpoint

	// Reduce aggregates the stream between Transform and Target. Nil passes
	// straight through.
	//
	// It DRAINS the source before the first row reaches the destination -- and
	// that is inherent to aggregating, not a choice. What still holds is the
	// ceiling: memory is proportional to the number of GROUPS, never to the
	// number of records.
	Reduce *Reduce

	// Stages replaces Transform and Reduce with an ordered list, for a
	// pipeline that needs more than one of each. See Stage.
	//
	// Declaring it together with either of them is an error: they describe the
	// same thing, and one would lose in silence.
	Stages []Stage

	// Name appears in logs. Defaults to provider/entity.
	Name string

	// Flags registers extra command-line flags before parsing, for a fetcher
	// that takes parameters of its own.
	Flags func(*flag.FlagSet)

	// Run is what the Brevis engine knows about this execution: whether it is
	// the first, the parameters it was dispatched with, which run it is.
	//
	// Filled in from the environment before Before runs, and zero when the
	// fetcher runs by hand. Read it if it helps; ignoring it costs nothing.
	//
	//	Before: func(ctx context.Context, p *sdk.Pipeline) error {
	//		if p.Run.Params["load_full"] == "true" {
	//			p.Source.URL += "&full=1"
	//		}
	//		return nil
	//	}
	Run RunContext

	// Before runs after flags are parsed and before the fetch, for a source
	// whose URL depends on those flags or on Run.
	Before func(ctx context.Context, p *Pipeline) error

	// Meter receives this pipeline's numbers. Nil is the normal case and costs
	// nothing.
	//
	// Setting it does NOT require writing a single counter: everything the SDK
	// already tracks -- records, rows, pages, HTTP attempts, bytes, the extract
	// and load durations -- is routed to it automatically. It is there for the
	// numbers only this pipeline knows.
	//
	//	m := otelmeter.New(ctx)         // from sdk/metrics/otel
	//	defer m.Close(ctx)
	//	sdk.Run(sdk.Pipeline{Meter: m, ...})
	Meter Meter
}

func (p Pipeline) name() string {
	if p.Name != "" {
		return p.Name
	}
	if p.Target.To != nil {
		return p.Target.To.Describe()
	}
	return "pipeline"
}

// Run runs a Pipeline as a command: it parses flags, sets up logging,
// honours -dry-run, prints the result and exits non-zero on failure.
//
// It calls os.Exit, so it belongs in main. Use Execute to keep control.
func Run(p Pipeline) {
	if err := Execute(context.Background(), &p, os.Args[1:]); err != nil {
		slog.Error("failed", "pipeline", p.name(), "error", err)
		os.Exit(1)
	}
}

// Execute is Run without the exit: it parses the arguments given and
// returns the error instead of terminating. This is what tests call.
func Execute(ctx context.Context, p *Pipeline, args []string) error {
	fs := flag.NewFlagSet(p.name(), flag.ContinueOnError)

	var (
		dryRun  = fs.Bool("dry-run", false, "extract, map and print the first records without writing")
		sample  = fs.Int("sample", 5, "how many records -dry-run prints")
		verbose = fs.Bool("v", false, "log at debug level")
		preview = fs.Int("preview", 0, "print the first N records as a table once the extract finishes")
	)
	if p.Flags != nil {
		p.Flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := LogLevel()
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// Read before Before, so a hook can act on it.
	p.Run = RunContextFromEnv()
	if p.Run.FromEngine() {
		slog.InfoContext(ctx, "running under Brevis",
			append([]any{"pipeline", p.name()}, p.Run.Args()...)...)
	}

	// The flag only turns the preview on; a pipeline that asked for one in
	// code keeps it, so running without the flag does not silently disable
	// what the fetcher configured.
	if *preview > 0 {
		p.Source.Preview = *preview
	}

	if p.Before != nil {
		if err := p.Before(ctx, p); err != nil {
			return err
		}
	}

	if *dryRun {
		return runDryRun(ctx, p, *sample)
	}

	return runPipeline(ctx, p)
}

// runPipeline is Execute after the flags: extract, transform, load, and the
// one line of log that says what happened.
//
// Separate from Execute because Execute installs the default logger, and a
// test that wants to read what was logged cannot do that through a function
// that replaces the logger first. The log line here is the whole of a
// fetcher's observability, so it is the part that most needs a test.
func runPipeline(ctx context.Context, p *Pipeline) error {
	rep := newReporter(p.Run)
	rep.announce(p.name())

	// The declaration is checked against the target BEFORE the extraction.
	//
	// The same check runs again in the Load, and that is not waste: between the
	// two the table can change, and the Load's is the one that decides. What
	// this first one buys is the vendor's quota -- finding out in the Load that
	// a column does not match means having spent the whole window on it.
	rep.started(PhaseCheck)
	stages, err := p.build()
	if err != nil {
		rep.finished(PhaseCheck, StateFailed, nil)
		return err
	}
	if err := checkDestination(ctx, p.Target); err != nil {
		rep.finished(PhaseCheck, StateFailed, nil)
		return err
	}
	rep.finished(PhaseCheck, StateDone, nil)

	rep.started(PhaseSource)
	data, cp, err := extrairComCheckpoint(ctx, p)
	if err != nil {
		rep.finished(PhaseSource, StateFailed, nil)
		return err
	}

	// The stages are measured where the work HAPPENS, not where the call
	// appears in the code. The chain is lazy: Extract returns an iterator and
	// the Load is what pulls it, so timing the three calls would report
	// "extract: 3ms" on a forty-minute extraction.
	//
	// The extract ends when the stream runs dry. Transform has no clock of its
	// own -- it runs per record, interleaved -- so it reports no duration at
	// all: a missing number beats a wrong one. What it reports is what only it
	// knows, how many records went in and how many came out.
	counts := make([]StageResult, len(stages))

	// One phase per stage, in the order they run, so the screen shows the
	// pipeline the consumer declared instead of one box called "transform".
	//
	// The positions are reserved UP FRONT: the source is announced before the
	// stages exist as boxes, and the target after them, so their indices have to
	// be known before the first record moves.
	if rep.on {
		indicesDosEstagios := make([]int, len(stages))
		for i := range stages {
			indicesDosEstagios[i] = rep.nextIndex()
		}
		targetIndex := rep.nextIndex()

		for i, st := range stages {
			rep.startedAtIndex(st.kind, indicesDosEstagios[i])
		}
		applyStages(data, stages, counts, p.Source.From.Describe())

		data.Records = onExhausted(data.Records, func() {
			rep.finished(PhaseSource, StateDone, sourceNumbers(data, p))
			for i, st := range stages {
				rep.finishedAtIndex(st.kind, indicesDosEstagios[i], StateDone,
					stageNumbers(counts[i]))
			}
			// Without batches nothing has been written yet: the Write only
			// happens once the stream runs dry. With batches it already
			// started, and the phase was opened below.
			if p.Target.FlushEvery == 0 {
				rep.startedAtIndex(PhaseTarget, targetIndex)
			}
		})
		if p.Target.FlushEvery > 0 {
			rep.startedAtIndex(PhaseTarget, targetIndex)
		}
	} else {
		applyStages(data, stages, counts, p.Source.From.Describe())
	}

	res, err := loadWith(ctx, data, p.Target, p.Run)
	if res != nil {
		// After the load: the depot is written while the stream runs, so only
		// now is it known whether it made it to the end.
		cp.apply(res)
		res.Stages = counts

		// The result comes back on the failure path too, by design, so that
		// RowErrors is readable after a refusal. That makes the message the
		// one thing that has to tell the two apart: "loaded" on a load that
		// wrote nothing is a line somebody will grep for and believe, and at
		// INFO it does not even reach whoever watches for errors.
		args := append([]any{"pipeline", p.name()}, res.Args()...)
		if err != nil {
			slog.Error("load failed", append(args, "error", err)...)
		} else {
			slog.Info("loaded", args...)
		}

		for _, line := range res.RowErrors {
			slog.Error("row rejected", "detail", line)
		}
	}

	// After the log line and before the return, so a fetcher that panics on the
	// way out has still reported. It runs on the FAILURE path too: a run that
	// broke is the one a failure rate exists to count.
	report(p.Meter, p.name(), res, err)

	state := StateDone
	if err != nil {
		state = StateFailed
	}
	rep.finished(PhaseTarget, state, loadNumbers(res))
	return err
}

// montar resolves the stages and refuses what cannot run.
//
// It is PURE: no I/O, so the -dry-run can call exactly the same thing the real
// run calls. An aggregator that does not exist, a name that collides with a
// group field, or Stages declared next to Transform are assembly errors, and
// finding them after the extract would cost the vendor's window.
func (p *Pipeline) build() ([]Stage, error) {
	if err := p.Target.validate(); err != nil {
		return nil, err
	}
	stages, err := p.stages()
	if err != nil {
		return nil, err
	}
	for _, st := range stages {
		if err := st.validate(); err != nil {
			return nil, err
		}
	}
	return stages, nil
}

// applyStages wires the stages onto the stream, in order, counting what
// passes through each.
//
// The run and the -dry-run call THIS, and not two similar loops. That is the
// whole point: the dry-run used to apply `p.Transform` on its own, which is
// empty whenever Stages is declared -- so a Stages pipeline's preview showed
// the source's raw rows and called them records, with no warning. A preview
// that answers a different question with the same confidence is worse than no
// preview, because it is what people run INSTEAD of writing.
func applyStages(data *Data, stages []Stage, counts []StageResult, source string) {
	for i, st := range stages {
		data.Records = st.apply(data.Records, &counts[i], source)
	}
}

// onExhausted warns when the source has run out -- which is when the extract
// really finished, and not when Extract returned the iterator.
func onExhausted(rows iter.Seq2[Envelope, error], end func()) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		for env, err := range rows {
			if !yield(env, err) {
				return // the consumer gave up: the source did not run out
			}
		}
		end()
	}
}

// checkDestination asks the destination, if it knows how to answer.
//
// Optional on purpose: a directory of files has no schema to check, and Redshift
// would need a running cluster. A destination that cannot check early must not
// be forced to pretend it can.
func checkDestination(ctx context.Context, t Target) error {
	if err := t.validate(); err != nil {
		return err
	}
	checker, sabe := t.To.(core.DestinationChecker)
	if !sabe {
		return nil
	}
	return checker.CheckDestination(ctx, t.declaredColumns())
}

// runDryRun extracts and maps without writing, printing the first n
// records with the ingestion_id each would get.
//
// Every fetcher needs this on day one, and every fetcher used to rewrite it.
// It is also the cheapest way to see that Key picks the fields you meant:
// a wrong key is invisible until rows start duplicating.
func runDryRun(ctx context.Context, p *Pipeline, n int) error {
	start := time.Now()

	// The SAME assembly the real run does. A -dry-run that skipped it would
	// pass on a pipeline the run refuses -- and the refusal is the whole point
	// of checking before writing.
	//
	// checkDestination is NOT called here, and that is deliberate: it asks the
	// destination, which needs credentials a laptop may not have. A -dry-run
	// that demanded BigQuery access to print five rows would stop being the
	// cheap check it exists to be.
	stages, err := p.build()
	if err != nil {
		return err
	}

	data, err := Extract(ctx, p.Source)
	if err != nil {
		return err
	}

	// The stages run here too: a dry-run that printed untransformed records
	// would show a payload -- and an ingestion_id -- that is not what lands.
	counts := make([]StageResult, len(stages))
	applyStages(data, stages, counts, p.Source.From.Describe())

	// Provenance must be stamped the same way Load would, or the printed
	// ingestion_id would not be the one that lands.
	envelopes, err := collect(data, p.Target)
	if err != nil {
		return err
	}

	stats := data.Stats()
	_, _ = fmt.Fprintf(os.Stdout, "dry-run %s -> %s (%d records, %d page(s), %d attempt(s), %s)\n",
		p.name(), p.Target.To.Describe(), len(envelopes), stats.Pages, stats.Attempts,
		time.Since(start).Round(time.Millisecond))

	// Per stage, because "5,515 records" says nothing about where the other six
	// million went. With this, finding out is one line instead of bisecting the
	// pipeline by hand.
	for _, c := range counts {
		row := fmt.Sprintf("  %-10s %9d -> %9d", c.Kind, c.In, c.Out)
		if c.Kind == StageAggregate {
			row += fmt.Sprintf("   (%d groups)", c.Groups)
		}
		_, _ = fmt.Fprintln(os.Stdout, row)
	}
	_, _ = fmt.Fprintln(os.Stdout)

	for i, env := range envelopes {
		if i == n {
			_, _ = fmt.Fprintf(os.Stdout, "... and %d more\n", len(envelopes)-n)
			break
		}
		body, err := json.Marshal(env.Payload)
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}

		// The row is printed whole. Whatever the chain composed is what
		// lands, ingestion_id included -- so there is nothing left to compute
		// here, and nothing that could be printed and then not written.
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", body)
	}

	if len(envelopes) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "no records -- the source answered, but with no data")
	}
	return nil
}
