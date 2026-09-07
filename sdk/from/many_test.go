package from_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// fonteFalsa entrega N registros, ou falha.
type fakeSource struct {
	name     string
	lines    int
	openErr  error
	errAfter int // > 0: it fails after N rows
}

func (f fakeSource) Describe() string { return f.name }

func (f fakeSource) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return func(yield func(sdk.Envelope, error) bool) {
		for i := 0; i < f.lines; i++ {
			if f.errAfter > 0 && i == f.errAfter {
				yield(sdk.Envelope{}, fmt.Errorf("%s quebrou na linha %d", f.name, i))
				return
			}
			if !yield(sdk.Envelope{Payload: map[string]any{"origem": f.name, "i": i}}, nil) {
				return
			}
		}
	}, nil
}

func drain(t *testing.T, source sdk.Reader, stats *sdk.Stats) ([]map[string]any, error) {
	t.Helper()
	data, err := sdk.Extract(context.Background(), sdk.Source{From: source, Stats: stats})
	if err != nil {
		return nil, err
	}
	var lines []map[string]any
	for env, err := range data.Records {
		if err != nil {
			return lines, err
		}
		lines = append(lines, env.Payload.(map[string]any))
	}
	return lines, nil
}

// TestManyJoinsTheSources: o caso base.
func TestManyJoinsTheSources(t *testing.T) {
	lines, err := drain(t, from.Many{Sources: []sdk.Reader{
		fakeSource{name: "a", lines: 2},
		fakeSource{name: "b", lines: 3},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Errorf("%d linhas, esperado 5", len(lines))
	}
}

// TestManySequentialKeepsTheOrder: with Workers 0 or 1 the sequence is
// deterministic, and that is why it is the default. Concurrency is opt-in
// precisely because it gives that up.
func TestManySequentialKeepsTheOrder(t *testing.T) {
	sources := []sdk.Reader{
		fakeSource{name: "a", lines: 2},
		fakeSource{name: "b", lines: 2},
		fakeSource{name: "c", lines: 2},
	}
	var previous string
	for attempt := 0; attempt < 20; attempt++ {
		lines, err := drain(t, from.Many{Sources: sources}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, l := range lines {
			order = append(order, fmt.Sprintf("%s%v", l["origem"], l["i"]))
		}
		current := strings.Join(order, ",")
		if current != "a0,a1,b0,b1,c0,c1" {
			t.Fatalf("ordem = %s", current)
		}
		if attempt > 0 && current != previous {
			t.Fatalf("a ordem variou entre execuções: %s e %s", previous, current)
		}
		previous = current
	}
}

// TestManyAbortsByDefault: changing the default in silence would make a run that
// fails today start "working" with half the data.
func TestManyAbortsByDefault(t *testing.T) {
	_, err := drain(t, from.Many{Sources: []sdk.Reader{
		fakeSource{name: "boa", lines: 2},
		fakeSource{name: "ruim", openErr: fmt.Errorf("504")},
		fakeSource{name: "outra", lines: 2},
	}}, nil)
	if err == nil {
		t.Fatal("o padrão tolerou uma falha")
	}
	if !strings.Contains(err.Error(), "ruim") {
		t.Errorf("o erro não nomeia a origem: %v", err)
	}
}

// TestManyContinuesAndSaysWhichFailed is the whole of item 2 in one test.
//
// On a fan-out of thousands of sources, "the run failed" is not information:
// what fixes it is knowing WHICH failed, so those get reprocessed and the others
// do not.
func TestManyContinuesAndSaysWhichFailed(t *testing.T) {
	var stats sdk.Stats
	lines, err := drain(t, from.Many{
		Sources: []sdk.Reader{
			fakeSource{name: "a", lines: 2},
			fakeSource{name: "quebrada", openErr: fmt.Errorf("504 do fornecedor")},
			fakeSource{name: "c", lines: 3},
		},
		OnError: sdk.ContinueOnError,
	}, &stats)
	if err != nil {
		t.Fatalf("ContinueOnError abortou: %v", err)
	}
	if len(lines) != 5 {
		t.Errorf("%d linhas, esperado 5 -- as boas têm de sobreviver à ruim", len(lines))
	}
	if len(stats.FailedSources) != 1 {
		t.Fatalf("falhas = %v, esperado uma", stats.FailedSources)
	}
	f := stats.FailedSources[0]
	if f.Source != "quebrada" || !strings.Contains(f.Err, "504") {
		t.Errorf("a falha não diz qual nem por quê: %+v", f)
	}
}

// TestAFailureMidwayThroughASourceCountsToo: a source that breaks on row 3
// failed just as much as one that never opened.
func TestAFailureMidwayThroughASourceCountsToo(t *testing.T) {
	var stats sdk.Stats
	lines, err := drain(t, from.Many{
		Sources: []sdk.Reader{
			fakeSource{name: "meia", lines: 10, errAfter: 3},
			fakeSource{name: "inteira", lines: 2},
		},
		OnError: sdk.ContinueOnError,
	}, &stats)
	if err != nil {
		t.Fatal(err)
	}
	// The 3 the source delivered before breaking count: they were read.
	if len(lines) != 5 {
		t.Errorf("%d linhas, esperado 5 (3 da meia + 2 da inteira)", len(lines))
	}
	if len(stats.FailedSources) != 1 || stats.FailedSources[0].Source != "meia" {
		t.Errorf("falhas = %v", stats.FailedSources)
	}
}

// TestEveryySourceFailingIsNotZeroRows: zero records from N good sources is a
// result; zero because all N failed is a broken run, and the two must not
// podem parecer a mesma coisa.
func TestEveryySourceFailingIsNotZeroRows(t *testing.T) {
	_, err := drain(t, from.Many{
		Sources: []sdk.Reader{
			fakeSource{name: "a", openErr: fmt.Errorf("504")},
			fakeSource{name: "b", openErr: fmt.Errorf("503")},
		},
		OnError: sdk.ContinueOnError,
	}, nil)
	if err == nil {
		t.Fatal("todas as origens falharam e a execução deu certo")
	}
	if !strings.Contains(err.Error(), "all 2 sources failed") {
		t.Errorf("o erro não diz que foram todas: %v", err)
	}
}

// TestManyConcurrentReadsEverything: with concurrency the order changes, the set
// does not.
func TestManyConcurrentReadsEverything(t *testing.T) {
	var sources []sdk.Reader
	for i := 0; i < 50; i++ {
		sources = append(sources, fakeSource{name: fmt.Sprintf("f%02d", i), lines: 4})
	}

	lines, err := drain(t, from.Many{Sources: sources, Workers: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 200 {
		t.Fatalf("%d linhas, esperado 200", len(lines))
	}
	seen := map[string]int{}
	for _, l := range lines {
		seen[l["origem"].(string)]++
	}
	if len(seen) != 50 {
		t.Errorf("%d origens apareceram, esperado 50", len(seen))
	}
	for name, n := range seen {
		if n != 4 {
			t.Errorf("%s entregou %d linhas", name, n)
		}
	}
}

// TestManySumsTheCounters: the result's counters have to describe the whole
// read, and not the last source.
func TestManySumsTheCounters(t *testing.T) {
	var stats sdk.Stats
	if _, err := drain(t, from.Many{
		Sources: []sdk.Reader{contadora{2}, contadora{3}, contadora{5}},
		Workers: 3,
	}, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 10 {
		t.Errorf("Pages = %d, esperado 10 (2+3+5)", stats.Pages)
	}
}

// countingSource fills the Stats it receives, like a real driver.
type contadora struct{ pages int }

func (c contadora) Describe() string { return fmt.Sprintf("contadora(%d)", c.pages) }
func (c contadora) Read(_ context.Context, opt sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		if opt.Stats != nil {
			opt.Stats.Pages = c.pages
		}
		yield(sdk.Envelope{Payload: map[string]any{"n": c.pages}}, nil)
	}, nil
}

// TestManyStopsReadingWhenTheConsumerStops: a break in the consumer's loop must
// not leave a goroutine stuck writing into a channel nobody reads.
func TestManyStopsReadingWhenTheConsumerStops(t *testing.T) {
	var sources []sdk.Reader
	for i := 0; i < 20; i++ {
		sources = append(sources, fakeSource{name: fmt.Sprintf("f%d", i), lines: 1000})
	}

	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Many{Sources: sources, Workers: 4},
	})
	if err != nil {
		t.Fatal(err)
	}

	feito := make(chan struct{})
	go func() {
		defer close(feito)
		n := 0
		for _, err := range data.Records {
			if err != nil {
				t.Error(err)
				return
			}
			n++
			if n == 10 {
				break
			}
		}
	}()

	select {
	case <-feito:
	case <-timeout():
		t.Fatal("a iteração não terminou depois do break; alguma goroutine ficou presa")
	}
}

// TestManyRefusesInvalidConfiguration.
func TestManyRefusesInvalidConfiguration(t *testing.T) {
	if _, err := drain(t, from.Many{}, nil); err == nil {
		t.Error("aceitou zero origens")
	}
	if _, err := drain(t, from.Many{Sources: []sdk.Reader{nil}}, nil); err == nil {
		t.Error("aceitou uma origem nil")
	}
}

func timeout() <-chan struct{} {
	c := make(chan struct{})
	go func() { time.Sleep(10 * time.Second); close(c) }()
	return c
}

// TestDiscoverBuildsTheSourcesInsideThePipeline is the second half of item 9.
//
// The list is sometimes only known at run time. Built before sdk.Run, it sits
// outside the pipeline: no retry, no timeout, no log, and no appearance in the
// Result when it fails.
func TestDiscoverBuildsTheSourcesInsideThePipeline(t *testing.T) {
	lines, err := drain(t, from.Many{
		Discover: func(context.Context) ([]sdk.Reader, error) {
			return []sdk.Reader{
				fakeSource{name: "descoberta-a", lines: 2},
				fakeSource{name: "descoberta-b", lines: 3},
			}, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Errorf("%d linhas, esperado 5", len(lines))
	}
}

// TestAFailingDiscoverIsAnExtractError: its error is treated like
// any other extract error, and not as a panic in a main before anything
// starts.
func TestAFailingDiscoverIsAnExtractError(t *testing.T) {
	_, err := drain(t, from.Many{
		Discover: func(context.Context) ([]sdk.Reader, error) {
			return nil, fmt.Errorf("o endpoint que lista as partições devolveu 503")
		},
	}, nil)
	if err == nil {
		t.Fatal("a descoberta falhou e a execução seguiu")
	}
	if !strings.Contains(err.Error(), "discovering the sources") {
		t.Errorf("o erro não diz o que falhou: %v", err)
	}
}

// TestAnEmptyDiscoverIsNotZeroRecords: a run that read nothing because
// there was nothing to read is different from one that did not know where to
// read.
func TestAnEmptyDiscoverIsNotZeroRecords(t *testing.T) {
	_, err := drain(t, from.Many{
		Discover: func(context.Context) ([]sdk.Reader, error) { return nil, nil },
	}, nil)
	if err == nil {
		t.Fatal("zero origens passou como zero registros")
	}
	if !strings.Contains(err.Error(), "returned no source at all") {
		t.Errorf("erro = %v", err)
	}
}

// TestDiscoverAndSourcesTogetherIsRefused: two lists of sources, and the loser
// would lose in silence.
func TestDiscoverAndSourcesTogetherIsRefused(t *testing.T) {
	_, err := drain(t, from.Many{
		Sources:  []sdk.Reader{fakeSource{name: "a", lines: 1}},
		Discover: func(context.Context) ([]sdk.Reader, error) { return nil, nil },
	}, nil)
	if err == nil {
		t.Fatal("declarar os dois passou")
	}
}
