package from

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Many reads from N sources and delivers everything as a single sequence.
//
//	From: from.Many{
//	    Sources: sources,             // one per city, per account, per day
//	    Workers: 8,
//	    OnError: sdk.ContinueOnError,
//	},
//
// Every ETL that reads from many sources writes the same loop: iterate, tolerate
// some failures, record which failed, accumulate. This is that loop, written
// once.
//
// # Failure tolerance, and why it is not the default
//
// With AbortOnError -- the default -- the first failure stops everything, which
// is what the SDK has always done. In a fan-out over thousands of sources that
// is expensive: read number 3,000 brings down the 1,803 that had already
// succeeded, and the next run redoes all 3,000.
//
// With ContinueOnError, a source that fails is recorded in Result.FailedSources
// and the read carries on. It is the same policy the load already has for a bad
// row -- it reports it in ErrorRows and continues -- and the asymmetry between
// the two sides was what was missing.
//
// The default stays "abort" because changing it silently would turn a run that
// fails today into one that "succeeds" with half the data.
//
// # Ordering
//
// With Workers at 0 or 1, the sources are read in order and the sequence is
// deterministic -- two runs over the same sources produce the same sequence.
//
// Above that, NO. Records arrive in the order the sources answer, and that
// changes between runs. It does not affect the ingestion_id, which comes from
// the record's fields and not its position; it affects the preview, and anything
// that depends on order. That is why concurrency is opt-in.
type Many struct {
	// Sources são as origens. Obrigatório, ou Discover.
	Sources []core.Reader

	// Discover monta as origens em runtime, dentro do pipeline.
	//
	//	Discover: func(ctx context.Context) ([]sdk.Reader, error) {
	//	    // a GET that lists the partitions, and one source per partition
	//	}
	//
	// The list is sometimes only known at run time -- one source per partition,
	// per account, per day. Built BEFORE sdk.Run it sits outside the pipeline:
	// no retry, no timeout, no log, and nothing in the Result when it fails.
	// Here it runs inside, and its error is the extract's error.
	//
	// Declaring both Discover and Sources is an error: two lists of sources, and
	// the one that loses loses in silence.
	Discover func(ctx context.Context) ([]core.Reader, error)

	// Workers is how many sources are read at once. Zero or 1 reads in order,
	// one at a time.
	//
	// The useful ceiling depends on what is on the other side: thousands of
	// requests to the same host run into the transport's connection pool, and
	// the source's RateLimiter still applies per source.
	Workers int

	// OnError says what to do when a source fails. Zero is AbortOnError.
	OnError core.FailurePolicy
}

// Describe satisfies core.Reader.
//
// It is called on the ERROR path -- it is where an invalid configuration's
// message goes to find the source's name -- so it must not panic on an invalid
// configuration. A test of exactly that found the problem: a nil source brought
// the process down at the moment the message mattered most.
func (m Many) Describe() string {
	if len(m.Sources) == 0 {
		return "many: (nenhuma origem)"
	}
	if m.Sources[0] == nil {
		return fmt.Sprintf("many: %d origens", len(m.Sources))
	}
	return fmt.Sprintf("many: %d origens, a primeira %s", len(m.Sources), m.Sources[0].Describe())
}

// Read satisfaz core.Reader.
func (m Many) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	if len(m.Sources) > 0 && m.Discover != nil {
		return nil, fmt.Errorf("from.Many declara Sources e Discover, e as duas montam a " +
			"lista de origens -- a que perde perderia em silêncio")
	}

	origens := m.Sources
	if m.Discover != nil {
		// Discovery happens HERE, inside Read, and not while the pipeline is
		// assembled: an error from it is an extract error, handled like any
		// other -- and not a panic in a main before anything has started.
		descobertas, err := m.Discover(ctx)
		if err != nil {
			return nil, fmt.Errorf("from.Many: discovering the sources: %w", err)
		}
		origens = descobertas
	}

	if len(origens) == 0 {
		if m.Discover != nil {
			return nil, fmt.Errorf("from.Many: Discover returned no source at all. Zero " +
				"sources is not the same as zero records: a run that read nothing because " +
				"there was nothing to read is different from one that did not know where " +
				"to read")
		}
		return nil, fmt.Errorf("from.Many needs at least one source in Sources, " +
			"or a Discover")
	}
	for i, s := range origens {
		if s == nil {
			return nil, fmt.Errorf("from.Many: source %d is nil", i)
		}
	}
	m.Sources = origens

	trabalhadores := m.Workers
	if trabalhadores < 1 {
		trabalhadores = 1
	}
	if trabalhadores > len(m.Sources) {
		trabalhadores = len(m.Sources)
	}

	inicio := time.Now()
	return func(yield func(core.Envelope, error) bool) {
		ctx, cancelar := context.WithCancel(ctx)
		defer cancelar()

		// Every source has its OWN Stats, and they are summed at the end. One
		// pointer shared between goroutines would be a data race -- and -race
		// would find it, but only after somebody wrote the test.
		var mu sync.Mutex
		var falhas []core.SourceFailure
		total := core.Stats{}

		type resultado struct {
			env core.Envelope
			err error
			// origem is filled only when err comes from OPENING the source,
			// because that is when it is possible to say which one failed.
			origem core.Reader
		}

		fila := make(chan int)
		saida := make(chan resultado)

		var wg sync.WaitGroup
		for w := 0; w < trabalhadores; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range fila {
					fonte := m.Sources[i]

					// One Stats per source, summed at the end: that way the
					// result's counters describe the whole read and not the
					// last source.
					porOrigem := core.Stats{}
					opcoes := opt
					opcoes.Stats = &porOrigem
					// The preview belongs to the set and is assembled up here;
					// asking each source for one would print N tables.
					opcoes.Preview = 0

					linhas, err := fonte.Read(ctx, opcoes)
					if err != nil {
						select {
						case saida <- resultado{err: err, origem: fonte}:
						case <-ctx.Done():
						}
						continue
					}

					for env, err := range linhas {
						select {
						case saida <- resultado{env: env, err: err, origem: fonte}:
						case <-ctx.Done():
							return
						}
						if err != nil {
							break
						}
					}

					mu.Lock()
					total.Pages += porOrigem.Pages
					total.Attempts += porOrigem.Attempts
					total.Bytes += porOrigem.Bytes
					mu.Unlock()
				}
			}()
		}

		go func() {
			defer close(fila)
			for i := range m.Sources {
				select {
				case fila <- i:
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() { wg.Wait(); close(saida) }()

		linhas := 0
		var amostra []any
		abortou := false

		for r := range saida {
			if r.err != nil {
				if m.OnError != core.ContinueOnError {
					yield(core.Envelope{}, fmt.Errorf("%s: %w", r.origem.Describe(), r.err))
					abortou = true
					cancelar()
					break
				}
				mu.Lock()
				falhas = append(falhas, core.SourceFailure{
					Source: r.origem.Describe(), Err: r.err.Error(),
				})
				mu.Unlock()
				slog.WarnContext(ctx, "a source failed and was tolerated",
					"source", r.origem.Describe(), "error", r.err)
				continue
			}

			linhas++
			if opt.Preview > 0 && len(amostra) < opt.Preview {
				amostra = append(amostra, r.env.Payload)
			}
			if !yield(r.env, nil) {
				cancelar()
				break
			}
		}

		// Drains whatever is in flight, so no goroutine stays stuck writing to a
		// channel nobody reads any more.
		cancelar()
		for range saida { //nolint:revive // draining is the point
		}

		if abortou {
			return
		}

		mu.Lock()
		total.FailedSources = falhas
		copiaFalhas := append([]core.SourceFailure(nil), falhas...)
		mu.Unlock()

		if opt.Stats != nil {
			sort.Slice(total.FailedSources, func(i, j int) bool {
				return total.FailedSources[i].Source < total.FailedSources[j].Source
			})
			opt.Stats.Pages += total.Pages
			opt.Stats.Attempts += total.Attempts
			opt.Stats.Bytes += total.Bytes
			opt.Stats.FailedSources = total.FailedSources
		}

		decorrido := time.Since(inicio)
		core.LogExtract(ctx, "many", m.Describe(), core.PreviewStats{
			Rows: linhas, Pages: total.Pages, Bytes: total.Bytes, Duration: decorrido,
		})
		if opt.Preview > 0 {
			core.WritePreview(opt.PreviewWriter, amostra, opt.PreviewBytes, core.PreviewStats{
				Rows: linhas, Pages: total.Pages, Bytes: total.Bytes, Duration: decorrido,
			})
		}

		// Zero records from N healthy sources is a result. Zero because all N
		// failed is a broken run, and the two must not look the same to whoever
		// reads the log.
		if linhas == 0 && len(copiaFalhas) == len(m.Sources) {
			yield(core.Envelope{}, core.ErrEverySourceFailed(len(m.Sources), copiaFalhas[0]))
		}
	}, nil
}
