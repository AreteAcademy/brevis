package execution_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/local"
)

type coletor struct {
	mu      sync.Mutex
	eventos []execution.Event
}

func (c *coletor) Evento(e execution.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventos = append(c.eventos, e)
}

// CRITERIO DE ACEITE DA PHASE 3 (secao 37):
//
//	Task A -> (Task B + Task C) -> Task D, with parallelism.
func TestAcceptanceCriterion_GoDAGWithParallelism(t *testing.T) {
	reg := execution.NewRegistry()

	var (
		mu     sync.Mutex
		ordem  []string
		emVoo  atomic.Int32
		picoBC atomic.Int32
	)

	registrar := func(nome string, dura time.Duration) {
		reg.MustRegister(execution.FuncTask{Nome: nome, Fn: func(ctx context.Context, _ execution.Input) error {
			n := emVoo.Add(1)
			for {
				p := picoBC.Load()
				if n <= p || picoBC.CompareAndSwap(p, n) {
					break
				}
			}
			defer emVoo.Add(-1)

			mu.Lock()
			ordem = append(ordem, nome)
			mu.Unlock()

			select {
			case <-time.After(dura):
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}})
	}
	registrar("task_a", 10*time.Millisecond)
	registrar("task_b", 150*time.Millisecond)
	registrar("task_c", 150*time.Millisecond)
	registrar("task_d", 10*time.Millisecond)

	w := wf.Workflow{
		Slug: "dag_go", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "a", Action: "task_a"},
			{ID: "b", Action: "task_b"},
			{ID: "c", Action: "task_c"},
			{ID: "d", Action: "task_d"},
		},
		Edges: []wf.Edge{
			{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}

	c := &coletor{}
	r := app.Runner{Go: local.NewGoExecutor(reg), Report: c}

	inicio := time.Now()
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	duracao := time.Since(inicio)

	// b e c rodaram JUNTAS: em serie o total passaria de 300ms
	if picoBC.Load() < 2 {
		t.Errorf("concurrency peak = %d; b and c should run in parallel", picoBC.Load())
	}
	if duracao > 280*time.Millisecond {
		t.Errorf("it took %s; in parallel it should land near 170ms, not 320ms", duracao)
	}

	// a first, d last -- the topological order was respected
	if ordem[0] != "task_a" {
		t.Errorf("first = %q, wanted task_a", ordem[0])
	}
	if ordem[len(ordem)-1] != "task_d" {
		t.Errorf("last = %q, wanted task_d", ordem[len(ordem)-1])
	}
	t.Logf("ordem: %v | duracao: %s | pico: %d", ordem, duracao.Round(time.Millisecond), picoBC.Load())
}

// The retry is PER NODE: redoing the whole workflow because the last step failed
// would throw away the work already finished.
func TestRetryIsPerNode(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32

	reg.MustRegister(execution.FuncTask{Nome: "instavel", Fn: func(context.Context, execution.Input) error {
		if tentativas.Add(1) < 3 {
			return fmt.Errorf("falha transitoria")
		}
		return nil
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "instavel"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 3, BackoffBase: time.Millisecond,
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("it should have succeeded on the 3rd attempt: %v", err)
	}
	if n := tentativas.Load(); n != 3 {
		t.Errorf("attempts = %d, wanted 3", n)
	}
}

func TestRetryGivesUpAfterTheLimit(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32
	reg.MustRegister(execution.FuncTask{Nome: "sempre_falha", Fn: func(context.Context, execution.Input) error {
		tentativas.Add(1)
		return fmt.Errorf("falha permanente")
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "sempre_falha"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 2, BackoffBase: time.Millisecond,
	}
	if err := r.Run(context.Background(), w); err == nil {
		t.Fatal("esperava falha")
	}
	if n := tentativas.Load(); n != 2 {
		t.Errorf("attempts = %d, wanted exactly 2", n)
	}
}

// Cancelling the context stops the DAG and fires no retry -- retrying against a
// cancelamento e desperdicio.
func TestCancellingFiresNoRetry(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32
	reg.MustRegister(execution.FuncTask{Nome: "lenta", Fn: func(ctx context.Context, _ execution.Input) error {
		tentativas.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "lenta"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 5, BackoffBase: time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx, w)

	if n := tentativas.Load(); n != 1 {
		t.Errorf("attempts = %d; cancelling must not retry", n)
	}
}

// A step with no configured executor has to fail with a clear message, and not
// ser pulado em silencio.
func TestAStepWithNoExecutorFailsExplicitly(t *testing.T) {
	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "qualquer"}}}
	err := app.Runner{Report: &coletor{}}.Run(context.Background(), w)
	if err == nil {
		t.Fatal("expected an error, not silence")
	}
}

// The exit code has to survive the whole path: executor event -> error
// tipado -> persistencia. Sem isto, distinguir 127 (comando inexistente) de 2
// (an application error) just by looking at the log.
func TestAStepsErrorCarriesTheExitCode(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "sai", Nodes: []wf.Node{{ID: "falha", Run: "sh -c 'exit 3'"}}}

	erro := app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)
	if erro == nil {
		t.Fatal("esperava falha")
	}

	var passo *app.ErroDePasso
	if !errors.As(erro, &passo) {
		t.Fatalf("error %T does not carry the exit code", erro)
	}
	if passo.ExitCode != 3 || passo.NodeID != "falha" {
		t.Errorf("node=%q exit=%d, want a failure and 3", passo.NodeID, passo.ExitCode)
	}
}

// The error has to say WHY the step failed, not just that it did. "exited with
// codigo 127" e tecnicamente correto e inutil: a causa (`sh: xpto: not found`)
// went past through the events as log and was dropped right there.
func TestTheErrorCarriesStderrAndTheCodesHint(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "ausente", Nodes: []wf.Node{
		{ID: "fetch_data", Run: "sh -c 'comando_que_nao_existe_abc'"},
	}}

	erro := app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)
	if erro == nil {
		t.Fatal("esperava falha")
	}

	msg := erro.Error()
	if !strings.Contains(msg, "127") {
		t.Errorf("the message has no exit code: %q", msg)
	}
	if !strings.Contains(msg, "command not found") {
		t.Errorf("the message has no translation of the code: %q", msg)
	}
	if !strings.Contains(msg, "comando_que_nao_existe_abc") {
		t.Errorf("the message has no stderr line explaining the failure: %q", msg)
	}

	var passo *app.ErroDePasso
	if !errors.As(erro, &passo) || len(passo.Saida) == 0 {
		t.Fatalf("the error does not carry the output: %+v", passo)
	}
}

// A verbose process must not fill the database's error column: only the last
// lines ride along with the failure, which is where the cause almost always is.
func TestStderrIsKeptAsTheLastLines(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "verboso", Nodes: []wf.Node{
		{ID: "ruido", Run: "sh -c 'for i in 1 2 3 4 5 6 7 8 9 10; do echo line $i >&2; done; exit 9'"},
	}}

	erro := app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)

	var passo *app.ErroDePasso
	if !errors.As(erro, &passo) {
		t.Fatalf("error %T", erro)
	}
	if len(passo.Saida) != 5 {
		t.Errorf("it kept %d lines, want 5", len(passo.Saida))
	}
	if len(passo.Saida) > 0 && passo.Saida[len(passo.Saida)-1] != "line 10" {
		t.Errorf("last line = %q, want the most recent one", passo.Saida[len(passo.Saida)-1])
	}
	// A code with no special meaning gets no invented translation.
	if strings.Contains(erro.Error(), "(") {
		t.Errorf("code 9 should get no hint: %q", erro.Error())
	}
}

// Success stays error-free even with the process writing to stderr -- plenty of
// legitimate commands use stderr for progress.
func TestStderrOnASucceedingStepIsNotAFailure(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "avisos", Nodes: []wf.Node{
		{ID: "ok", Run: "sh -c 'echo aviso >&2; exit 0'"},
	}}
	if erro := (app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w); erro != nil {
		t.Errorf("a step with stderr and exit 0 became a failure: %v", erro)
	}
}

// O dbt imprime "Parsing Error" em STDOUT. Capturar so stderr deixava a falha
// as "exited with code 2", without the cause that was on the screen all along.
func TestAFailureWithNoStderrUsesStdout(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "dbt", Nodes: []wf.Node{{
		ID:  "run",
		Run: `sh -c 'echo "Running with dbt=1.10.3"; echo "Env var required but not provided: GOOGLE_PROJECT_ID"; exit 2'`,
	}}}

	erro := (app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w)
	if erro == nil {
		t.Fatal("esperava falha")
	}
	if !strings.Contains(erro.Error(), "Env var required") {
		t.Errorf("the cause, printed to stdout, did not reach the message: %q", erro.Error())
	}
}

// When there is stderr, it wins: it is where the program chose to report the
// error, and the normal output should not crowd the message out.
func TestStderrTakesPrecedenceOverStdout(t *testing.T) {
	exec, _ := local.New("local")
	w := wf.Workflow{Slug: "misto", Nodes: []wf.Node{{
		ID:  "run",
		Run: `sh -c 'echo "normal progress line"; echo "the real cause" >&2; exit 1'`,
	}}}

	erro := (app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w)

	msg := erro.Error()
	if !strings.Contains(msg, "the real cause") {
		t.Errorf("stderr did not arrive: %q", msg)
	}
	if strings.Contains(msg, "normal progress line") {
		t.Errorf("stdout came in alongside stderr: %q", msg)
	}
}

// The request, literally: with ten ready steps and five slots, five run and the
// others come in as slots open -- never six at once.
//
// Before, the dispatcher's limit counted RUNS: five runs with three parallel
// steps each gave fifteen pods in the cluster, not five.
func TestSlotsLimitSimultaneousSteps(t *testing.T) {
	const passos, teto = 10, 5

	var emVoo, pico int64
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "ocupa", Fn: func(ctx context.Context, in execution.Input) error {
		atual := atomic.AddInt64(&emVoo, 1)
		for {
			anterior := atomic.LoadInt64(&pico)
			if atual <= anterior || atomic.CompareAndSwapInt64(&pico, anterior, atual) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt64(&emVoo, -1)
		return nil
	}})

	// All at the SAME level: with no dependency between them, the runner fires all
	// ten at once if nothing holds it back.
	w := wf.Workflow{Slug: "paralelo"}
	for i := 0; i < passos; i++ {
		w.Nodes = append(w.Nodes, wf.Node{ID: fmt.Sprintf("p%d", i), Action: "ocupa"})
	}

	r := app.Runner{
		Go:     local.NewGoExecutor(reg),
		Report: &coletor{},
		Vagas:  make(chan struct{}, teto),
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if p := atomic.LoadInt64(&pico); p > teto {
		t.Errorf("pico de %d passos simultaneos, o teto e %d", p, teto)
	} else if p < teto {
		t.Errorf("a peak of %d: the slots went unused, the limit became serialization", p)
	}
}

// The semaphore belongs to the PROCESS, not to the workflow: two concurrent runs
// share the same ceiling, otherwise each run would get five pods of its own.
func TestSlotsAreSharedBetweenRuns(t *testing.T) {
	const teto = 3

	var emVoo, pico int64
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "ocupa", Fn: func(ctx context.Context, in execution.Input) error {
		atual := atomic.AddInt64(&emVoo, 1)
		for {
			anterior := atomic.LoadInt64(&pico)
			if atual <= anterior || atomic.CompareAndSwapInt64(&pico, anterior, atual) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt64(&emVoo, -1)
		return nil
	}})

	vagas := make(chan struct{}, teto)
	monta := func(slug string) wf.Workflow {
		w := wf.Workflow{Slug: slug}
		for i := 0; i < 5; i++ {
			w.Nodes = append(w.Nodes, wf.Node{ID: fmt.Sprintf("p%d", i), Action: "ocupa"})
		}
		return w
	}

	var wg sync.WaitGroup
	for _, slug := range []string{"a", "b"} {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			r := app.Runner{Go: local.NewGoExecutor(reg), Report: &coletor{}, Vagas: vagas}
			if err := r.Run(context.Background(), monta(slug)); err != nil {
				t.Error(err)
			}
		}(slug)
	}
	wg.Wait()

	if p := atomic.LoadInt64(&pico); p > teto {
		t.Errorf("a peak of %d with two runs; the process ceiling is %d", p, teto)
	}
}

// With no slots configured, nothing changes -- `brevis run`'s local mode must not
// gain a limit nobody asked for.
func TestWithNoSlotsThereIsNoLimit(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "nada", Fn: func(context.Context, execution.Input) error { return nil }})

	w := wf.Workflow{Slug: "livre", Nodes: []wf.Node{
		{ID: "a", Action: "nada"}, {ID: "b", Action: "nada"},
	}}
	if err := (app.Runner{Go: local.NewGoExecutor(reg), Report: &coletor{}}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

// The pod's name includes the attempt. Without that the retry finds the previous
// attempt's pod again, and since the executor ADOPTS an existing pod (so as not
// to bring up two identical ones when the process dies midway) it stays stuck to
// the broken pod. It happened in dev: a pod Pending for want of CPU was
// re-adopted on every retry, and the run never moved.
func TestTheAttemptReachesTheTask(t *testing.T) {
	var vistas []int
	espiao := &spyExecutor{aoExecutar: func(tk execution.TaskExec) {
		vistas = append(vistas, tk.Attempt)
	}}

	w := wf.Workflow{Slug: "w", Image: "img", Nodes: []wf.Node{{ID: "a", Run: "x"}}}
	_ = app.Runner{
		Pods: espiao, Report: &coletor{},
		MaxTentativas: 3, BackoffBase: time.Millisecond,
	}.Run(context.Background(), w)

	if len(vistas) != 3 {
		t.Fatalf("attempts observadas: %v", vistas)
	}
	for i, n := range vistas {
		if n != i {
			t.Errorf("attempt %d arrived as %d; the pod name would repeat", i, n)
		}
	}
}

// spyExecutor records what it received and always fails, to exercise the retry.
type spyExecutor struct{ aoExecutar func(execution.TaskExec) }

func (e *spyExecutor) Name() string { return "espiao" }
func (e *spyExecutor) Execute(_ context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	e.aoExecutar(t)
	ch := make(chan execution.Event, 1)
	ch <- execution.Event{Kind: execution.EventFailed, NodeID: t.NodeID, Message: "falhou"}
	close(ch)
	return ch, nil
}
func (e *spyExecutor) Cancel(context.Context, string) error { return nil }
