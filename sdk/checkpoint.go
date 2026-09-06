package sdk

import (
	"context"
	"fmt"
	"hash/fnv"
	"iter"
	"log/slog"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/checkpoint"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Checkpoint keeps this run's raw extract so that a SECOND ATTEMPT of the same
// run does not have to go back to the source.
//
//	sdk.Run(sdk.Pipeline{
//		Source:     /* ... */,
//		Checkpoint: sdk.Checkpoint{At: "gs://landing/_checkpoint", Store: gcs.New(c)},
//		Target:     /* ... */,
//	})
//
// What it buys is the vendor's quota. An extract that spent 4,803 requests and
// forty minutes should not be repeated because a column at the destination
// changed type.
//
// What it costs: the extraction stops being a single pass. The whole extract
// lands in the depot before the first row is loaded, and is then read back --
// one extra write and one extra read of the volume, on EVERY run, to rescue the
// one that fails. That is why it ships off.
//
// # When NOT to use it
//
// If the extract and the load are already two nodes of the DAG, this adds
// nothing: the engine already retries each node on its own, so a load that
// fails does not redo the extract, which is another node that succeeded. Write
// with to.Files, read Result.Objects, and pass it along. That is less code and
// gives two boxes on the screen instead of one.
//
// # Guarantees
//
//   - An incomplete depot is NEVER resumed. The manifest is written last;
//     without it the extract is redone.
//   - A depot only serves the run that wrote it. Nothing is reused between
//     runs, so no stale data enters as fresh.
//   - The ingestion_ids of a resumed run are IDENTICAL to the first attempt's.
//   - Failing to write the depot does not kill the run: it carries on and
//     warns.
type Checkpoint struct {
	// At is the root directory of the depots. Empty turns it off.
	//
	// The effective path carries the run and the pipeline underneath it,
	// because a run has several steps and two steps must not share a depot.
	At string

	// Store is the object storage backend, as in the drivers. Nil is the local
	// filesystem.
	Store core.Store
}

// checkpointState is what happened to the depot on this run. The fields are
// filled while the stream runs, and read once it ends.
type estadoCheckpoint struct {
	caminho       string
	reaproveitado bool
	erro          string
}

func (e *estadoCheckpoint) aplicar(r *Result) {
	if e == nil || r == nil {
		return
	}
	r.CheckpointPath = e.caminho
	r.CheckpointReused = e.reaproveitado
	r.CheckpointError = e.erro
}

// checkpointPath builds this run's depot:
//
//	{At}/{run_id}/{name}-{hash}/
//
// The run identifies the execution and the name identifies the step. The hash
// is there because the name becomes a path segment and has to be sanitised:
// without it, two pipelines whose names sanitise to the same thing would share
// a depot, and one would resume from the other's extract -- wrong data loaded
// in silence, which is the worst way to fail.
func (p *Pipeline) caminhoDoCheckpoint() string {
	nome := p.name()
	h := fnv.New32a()
	_, _ = h.Write([]byte(nome))
	return fmt.Sprintf("%s/%s/%s-%08x/",
		strings.TrimSuffix(p.Checkpoint.At, "/"), segmento(p.Run.ID), segmento(nome), h.Sum32())
}

// segment turns free text into a path component.
func segmento(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "pipeline"
	}
	return b.String()
}

// extractWithCheckpoint is runPipeline's Extract, with the depot in between.
func extrairComCheckpoint(ctx context.Context, p *Pipeline) (*Data, *estadoCheckpoint, error) {
	est := &estadoCheckpoint{}

	if p.Checkpoint.At == "" {
		d, err := Extract(ctx, p.Source)
		return d, est, err
	}

	// With no run id there is no stable key: every run would write somewhere
	// different and nothing would ever be reused. Saying so beats ignoring it
	// in silence -- somebody configured this expecting it to work.
	if p.Run.ID == "" {
		slog.WarnContext(ctx, "checkpoint desligado: sem "+core.EnvRunID+
			" nao ha chave estavel entre tentativas",
			"pipeline", p.name(), "checkpoint", p.Checkpoint.At)
		d, err := Extract(ctx, p.Source)
		return d, est, err
	}

	// Wrong configuration is an ERROR, not a warning: a scheme that does not
	// match the Store will never write, and carrying on with a warning would
	// hide that on every run until the day somebody needed to resume.
	dep, err := checkpoint.Novo(p.caminhoDoCheckpoint(), p.Checkpoint.Store)
	if err != nil {
		return nil, est, err
	}
	est.caminho = dep.Caminho()

	// On the first attempt there is nothing to resume -- the path carries the
	// run id, and this run starts here. Not looking avoids a warning per run
	// saying it did not find what could not exist.
	if p.Run.Attempt > 0 {
		if d, ok := retomar(ctx, p, dep, est); ok {
			return d, est, nil
		}
	}

	data, err := Extract(ctx, p.Source)
	if err != nil {
		return nil, est, err
	}

	// Prove writing works BEFORE spending the quota. The most common failure
	// is permissions, and finding it after the extraction would mean having
	// spent exactly what the checkpoint exists to save.
	if err := dep.Reservar(ctx, p.name(), p.Run.ID); err != nil {
		est.erro = err.Error()
		slog.WarnContext(ctx, "checkpoint indisponivel; a execucao segue sem ele",
			"pipeline", p.name(), "checkpoint", est.caminho, "erro", err)
		return data, est, nil
	}

	data.Records = materializar(ctx, dep, data.Records, p, est)
	return data, est, nil
}

// resume reads the previous attempt's depot, when it is whole.
func retomar(ctx context.Context, p *Pipeline, dep *checkpoint.Deposito,
	est *estadoCheckpoint) (*Data, bool) {

	m, err := dep.Manifesto(ctx)
	if err != nil {
		slog.InfoContext(ctx, "sem checkpoint utilizavel; refazendo o extract",
			"pipeline", p.name(), "checkpoint", est.caminho, "motivo", err)
		return nil, false
	}
	if err := dep.Conferir(ctx, m); err != nil {
		slog.WarnContext(ctx, "checkpoint incompleto; refazendo o extract",
			"pipeline", p.name(), "checkpoint", est.caminho, "motivo", err)
		return nil, false
	}

	est.reaproveitado = true
	slog.InfoContext(ctx, "checkpoint reaproveitado: a origem nao sera consultada",
		"pipeline", p.name(), "checkpoint", est.caminho,
		"registros", m.Registros, "tentativa", p.Run.Attempt)

	stats := p.Source.Stats
	if stats == nil {
		stats = &core.Stats{}
	}
	return &Data{
		Records: dep.Reler(ctx, m),
		source:  p.Source,
		start:   time.Now(),
		// Pages and attempts stay at zero, and that is the truth: this run
		// fetched no pages at all.
		stats: stats,
	}, true
}

// materialise drains the source into the depot and yields what was written.
//
// Reading it back is not waste: it makes the RESUME path run on every
// successful execution. A recovery path that only runs in an emergency is a
// path nobody has ever seen work.
func materializar(ctx context.Context, dep *checkpoint.Deposito,
	origem iter.Seq2[Envelope, error], p *Pipeline, est *estadoCheckpoint) iter.Seq2[Envelope, error] {

	nome, run := p.name(), p.Run.ID

	return func(yield func(Envelope, error) bool) {
		esc := dep.Escrever()
		proximo, parar := iter.Pull2(origem)
		defer parar()

		// degrade gives up on the depot without giving up on the run: it yields
		// what already became an object, what stayed in the buffer, and then
		// carries on straight from the source. It writes no manifest, so nobody
		// resumes from a crippled depot.
		degradar := func(causa error, pendente *Envelope) {
			est.erro = causa.Error()
			slog.WarnContext(ctx, "checkpoint interrompido; a execucao segue sem ele",
				"pipeline", nome, "checkpoint", est.caminho, "erro", causa)

			for env, err := range dep.Reler(ctx, esc.Gravadas()) {
				if !yield(env, err) {
					return
				}
			}
			for env, err := range esc.Pendentes() {
				if !yield(env, err) {
					return
				}
			}
			if pendente != nil && !yield(*pendente, nil) {
				return
			}
			for {
				env, err, ok := proximo()
				if !ok {
					return
				}
				if !yield(env, err) {
					return
				}
			}
		}

		for {
			env, err, ok := proximo()
			if !ok {
				break
			}
			if err != nil {
				// A origem falhou: o extract nao terminou, e sem manifesto
				// ninguem vai retomar deste deposito pela metade.
				yield(Envelope{}, err)
				return
			}
			if e := esc.Add(env); e != nil {
				degradar(e, &env) // nao entrou no buffer, entao vai a mao
				return
			}
			if esc.Cheio() {
				if e := esc.Despejar(ctx); e != nil {
					degradar(e, nil) // ja esta no buffer; Pendentes o cede
					return
				}
			}
		}

		if err := esc.Finish(ctx, nome, run); err != nil {
			degradar(err, nil)
			return
		}

		for env, err := range dep.Reler(ctx, esc.Gravadas()) {
			if !yield(env, err) {
				return
			}
		}
	}
}
