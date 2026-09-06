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
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/graph"
	"sync"
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
type Historico interface {
	PassoJaTeveSucesso(ctx context.Context, workflowSlug, nodeID string, exceto uuid.UUID) (bool, error)
}

type Persistidor interface {
	IniciarTask(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int) error
	TerminarTask(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int,
		status run.Status, exit *int, erro string, log string) error

	// RegistrarEtapas records the phases of an SDK step while it runs. It is
	// what makes the screen advance before the step finishes.
	RegistrarEtapas(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int,
		sdkVersao string, etapas json.RawMessage) error
}

// Runner runs a whole workflow.
//
// It holds TWO executors and picks per node: `run:` goes to the process one,
// `action:` resolves in the Go registry. The choice belongs to the runner and
// not to the executor, so each executor can go on ignoring that the other
// exists.
type Runner struct {
	Processo execution.Executor // atende `run:`; pode ser nil se so houver tasks Go
	Go       execution.Executor // atende `action:`; pode ser nil

	WorkDir string
	Env     map[string]string
	Report  Reporter

	// Timeout per node. Zero means no limit.
	Timeout time.Duration

	// MaxTentativas per node. Zero or 1 means a single attempt.
	MaxTentativas int
	BackoffBase   time.Duration

	// Persist and RunID are used together: without both, per-step state is not
	// recorded and the DAG in the UI shows up with no execution state.
	Persist Persistidor
	RunID   uuid.UUID

	// Params are this run's values. They reach the step's command through a
	// template (see execution.Renderizar) and the step's environment, so a
	// fetcher using the SDK sees them without being handed an argument.
	Params map[string]string

	// Trigger says why this Run exists: schedule, manual or backfill.
	Trigger string

	// LogicalDate is the slot this Run stands for. Nil on a manual trigger.
	LogicalDate *time.Time

	// Historico decides whether a step is running for the first time. Nil means
	// there is no way to know -- and then the step gets first=false, because
	// creating a table without being sure is worse than not creating it.
	Historico Historico

	// Vagas caps how many STEPS run at once -- in Kubernetes, how many pods
	// exist simultaneously. Nil means no limit.
	//
	// It has to be shared across every Runner in the process, which is why it
	// is injected rather than created here: the ceiling belongs to the CLUSTER,
	// not to one workflow. Without it, the dispatcher's concurrency limit
	// counted RUNS -- five runs with three parallel steps each gave fifteen
	// pods, not five.
	Vagas chan struct{}

	// TentativaDoRun is this RUN's attempt, counted by the dispatcher. It goes
	// into the pod name so a retry does not find the previous attempt's pod.
	TentativaDoRun int

	// Pods runs steps as pods in Kubernetes. When present it serves every step
	// that declares `image:` -- and the same DAG runs as a pod in the cluster
	// and as a process on a laptop, with no change to the YAML.
	Pods execution.Executor
}

// Run walks the graph by levels: everything inside a level runs in parallel,
// and the next level only starts once the previous one closes entirely.
//
// It stops at the FIRST failure in a level, without starting the next. Carrying
// on after an error would produce a partial result that looks complete -- which
// is how a pipeline ran 28 days late without anyone seeing it, in the system
// this one replaces.
func (r Runner) Run(ctx context.Context, w wf.Workflow) error {
	niveis, err := graph.Niveis(w)
	if err != nil {
		return err
	}
	porID := make(map[string]wf.Node, len(w.Nodes))
	for _, n := range w.Nodes {
		porID[n.ID] = n
	}

	for i, nivel := range niveis {
		if err := r.rodarNivel(ctx, w, nivel, porID); err != nil {
			return fmt.Errorf("nivel %d: %w", i+1, err)
		}
	}
	return nil
}

func (r Runner) rodarNivel(ctx context.Context, w wf.Workflow, nivel []string, porID map[string]wf.Node) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		erros []error
	)

	for _, id := range nivel {
		n := porID[id]

		wg.Add(1)
		go func(n wf.Node) {
			defer wg.Done()
			if err := r.rodarNo(ctx, w, n); err != nil {
				mu.Lock()
				erros = append(erros, err)
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()

	if len(erros) > 0 {
		return erros[0]
	}
	return nil
}

// rodarNo runs one node, with retry.
//
// The retry is PER NODE, and not only per Run as in the dispatcher: redoing the
// whole workflow because a `notify.sh` failed would throw away the work already
// finished.
func (r Runner) rodarNo(ctx context.Context, w wf.Workflow, n wf.Node) error {
	tentativas := r.MaxTentativas
	if tentativas < 1 {
		tentativas = 1
	}

	var ultima error
	for t := 1; t <= tentativas; t++ {
		// The slot is taken per ATTEMPT, not for the whole step: holding it
		// through the backoff would leave a cluster slot idle waiting on a
		// clock.
		libera, err := r.ocupar(ctx)
		if err != nil {
			return err
		}
		r.marcarInicio(ctx, n.ID, t-1)
		var saida string
		saida, ultima = r.tentar(ctx, w, n, t-1)
		r.marcarFim(ctx, n.ID, t-1, ultima, saida)
		libera()
		if ultima == nil {
			return nil
		}
		if t == tentativas {
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
				Message: fmt.Sprintf("tentativa %d/%d falhou, repetindo em %s", t, tentativas, espera),
			})
		}
		select {
		case <-time.After(espera):
		case <-ctx.Done():
			return ultima
		}
	}
	return ultima
}

// ocupar takes a slot and returns the function that frees it.
//
// It blocks until there is room, and that is the asked-for behaviour: with ten
// ready steps and five slots, five run and the rest wait, entering as slots
// open. Refusing instead of waiting would turn an excess of work into a
// failure, when it is only a queue.
func (r Runner) ocupar(ctx context.Context) (func(), error) {
	if r.Vagas == nil {
		return func() {}, nil
	}
	select {
	case r.Vagas <- struct{}{}:
		var uma sync.Once
		return func() { uma.Do(func() { <-r.Vagas }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// marcarInicio and marcarFim only record when there is both a persister AND a
// RunID. A write failure does not interrupt the run: losing a step's record is
// bad, but aborting the workflow over it is worse.
func (r Runner) marcarInicio(ctx context.Context, nodeID string, tentativa int) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	if err := r.Persist.IniciarTask(ctx, r.RunID, nodeID, tentativa); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: nodeID, Stream: "stderr",
			Message: "nao consegui registrar o inicio do passo: " + err.Error(),
		})
	}
}

// marcarEtapas records the phases as they advance. Failing here does NOT bring
// the step down: the screen is informative, and the truth about a step remains
// its exit code. Trading a run for a screen update would be the wrong bargain.
//
// It is only called after a marker has been recognised, which is what keeps an
// ordinary step from paying a database round trip per log line. Checking again
// here would be a verification that cannot fail.
func (r Runner) marcarEtapas(ctx context.Context, nodeID string, tentativa int, c *coletorDeEtapas) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	dados, err := json.Marshal(c.Etapas)
	if err != nil {
		return
	}
	_ = r.Persist.RegistrarEtapas(ctx, r.RunID, nodeID, tentativa, c.Versao, dados)
}

func (r Runner) marcarFim(ctx context.Context, nodeID string, tentativa int, causa error, log string) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	status, msg := run.StatusSuccess, ""
	var exit *int
	if causa != nil {
		status, msg = run.StatusFailed, causa.Error()
		var passo *ErroDePasso
		// Exit 0 is not recorded: a Go task that fails has no process, and a
		// zero in that column would read as "finished fine" next to a failed
		// status.
		if errors.As(causa, &passo) && passo.ExitCode != 0 {
			exit = &passo.ExitCode
		}
	}
	if err := r.Persist.TerminarTask(ctx, r.RunID, nodeID, tentativa, status, exit, msg, log); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: nodeID, Stream: "stderr",
			Message: "nao consegui registrar o fim do passo: " + err.Error(),
		})
	}
}

// ErroDePasso is a step's failure, with the context needed to understand it
// without opening a log: the exit code, what it means, and the last lines the
// process wrote to stderr.
//
// Before, all that survived was "exited with code 127" -- technically correct
// and useless. The cause (`/bin/sh: python: not found`) went through the events
// as a log line and was dropped right there, so the screen showed the symptom
// without the explanation.
type ErroDePasso struct {
	NodeID   string
	ExitCode int
	Mensagem string

	// Saida is the last few lines of stderr. Only the last ones, and not all of
	// them, because a chatty process would fill the database's error column --
	// and the cause is almost always at the end.
	Saida []string
}

func (e *ErroDePasso) Error() string {
	cabecalho := fmt.Sprintf("step %q: %s", e.NodeID, e.Mensagem)
	if dica := dicaDoCodigo(e.ExitCode); dica != "" {
		cabecalho += " (" + dica + ")"
	}
	if len(e.Saida) == 0 {
		return cabecalho
	}
	return cabecalho + "\n" + strings.Join(e.Saida, "\n")
}

// dicaDoCodigo translates the exit codes the shell reserves. They are the most
// confusing ones: 127 is not an application error but a missing command -- the
// difference between looking for a defect in the code and looking in the
// image.
func dicaDoCodigo(c int) string {
	switch c {
	case 126:
		return "comando sem permissao de execucao"
	case 127:
		return "comando nao encontrado — verifique se ele existe na imagem do worker"
	case 130:
		return "interrompido por SIGINT"
	case 137:
		return "morto por SIGKILL — normalmente falta de memoria"
	case 143:
		return "encerrado por SIGTERM"
	case -1:
		return "encerrado por sinal, sem codigo de saida"
	}
	return ""
}

// linhasDeContexto is how many lines of stderr travel with a failure. Five
// cover a short stack trace or a command's closing message without drowning the
// screen.
const linhasDeContexto = 5

// tentar runs the step once and returns the complete output (capped) along with
// the outcome. The output comes back on success too: a step that finished fine
// but produced very little is a signal, and it is only visible in the log.
func (r Runner) tentar(ctx context.Context, w wf.Workflow, n wf.Node, tentativa int) (string, error) {
	// Asked once per attempt, and not inside montar, because building a task
	// must not do I/O. A retry of the same run does not reopen the first
	// execution: if attempt 1 wrote a row, PassoJaTeveSucesso already answers
	// yes; if it failed, this is still the first, which is right.
	primeira := r.primeiraExecucao(ctx, w.Slug, n.ID)

	exec, tarefa, err := r.montar(w, n, tentativa, primeira)
	if err != nil {
		return "", err
	}

	eventos, err := exec.Execute(ctx, tarefa)
	if err != nil {
		return "", fmt.Errorf("step %q: %w", n.ID, err)
	}

	var falha *ErroDePasso
	var stderr, stdout []string

	// The whole output (capped) goes to the database. The 5-line windows below
	// still exist for the error MESSAGE, which has to fit in a Slack alert;
	// this one keeps what the operator will want to read later, when the pod
	// that produced it is long gone.
	var completa janela

	// The phases the step announces, when it is an SDK pipeline.
	var etapas coletorDeEtapas

	for e := range eventos {
		// A marked line is the SDK talking to the engine, not the program's
		// output. It becomes a phase on the screen and does NOT enter the log
		// or the Report: whoever looks wants to see the phases, not the JSON
		// that carried them.
		if e.Kind == execution.EventLog {
			if linha := strings.TrimSpace(e.Message); linha != "" && etapas.linha(linha) {
				r.marcarEtapas(ctx, n.ID, tentativa, &etapas)
				continue
			}
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
			if linha := strings.TrimSpace(e.Message); linha != "" {
				completa.Escrever(linha)
				alvo := &stdout
				if e.Stream == "stderr" {
					alvo = &stderr
				}
				*alvo = append(*alvo, linha)
				if len(*alvo) > linhasDeContexto {
					*alvo = (*alvo)[1:]
				}
			}
		}
		if e.Kind == execution.EventFailed {
			falha = &ErroDePasso{NodeID: n.ID, ExitCode: e.ExitCode, Mensagem: e.Message}
		}
	}
	if falha == nil {
		return completa.String(), nil
	}
	// stderr first: when it exists, it is where the program meant to report an
	// error. stdout only enters in its absence, so the message is not filled
	// with the ordinary output of a command that merely ended badly.
	falha.Saida = stderr
	if len(falha.Saida) == 0 {
		falha.Saida = stdout
	}
	return completa.String(), falha
}

// Prefix of the variables that describe THIS run, kept apart from the
// BREVIS_SDK_ that configures the SDK: one says what the SDK does, the other
// what this particular dispatch is.
//
// This is not a private channel. The step's process can read its own
// environment, and somebody will. What is promised is that it does NOT HAVE TO
// -- not that it cannot.
const (
	envRunID          = "BREVIS_RUN_ID"
	envRunFirst       = "BREVIS_RUN_FIRST"
	envRunAttempt     = "BREVIS_RUN_ATTEMPT"
	envRunTrigger     = "BREVIS_RUN_TRIGGER"
	envRunLogicalDate = "BREVIS_RUN_LOGICAL_DATE"
	envRunParams      = "BREVIS_RUN_PARAMS"
)

// contextoDoRun builds what the engine knows about this run and the step does not.
//
// primeira is resolved beforehand, by the caller, because it needs a database
// round trip and building a task must not do I/O.
func (r Runner) contextoDoRun(nodeID string, primeira bool, tentativa int) map[string]string {
	env := map[string]string{}

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
		env[envRunFirst] = strconv.FormatBool(primeira)
		// Starts at zero, like the task_runs.attempt column.
		env[envRunAttempt] = strconv.Itoa(tentativa)
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

// mesclarEnv junta o ambiente do runner com o desta execucao.
//
// The runner's wins a collision: if somebody set BREVIS_RUN_PARAMS in the
// configuration they meant to, and the engine does not overwrite explicit
// configuration.
// mesclarEnv merges the maps in increasing order of precedence, except the
// first: `base` (the engine's global environment) beats the run context, which
// is how it has always been, and whatever comes after beats `base`.
func mesclarEnv(base, execucao map[string]string, acima ...map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(execucao))
	for k, v := range execucao {
		out[k] = v
	}
	for k, v := range base {
		out[k] = v
	}
	for _, m := range acima {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// primeiraExecucao pergunta ao historico se este passo ja teve sucesso.
//
// With no history configured the answer is "not the first": creating a table
// without being sure is worse than not creating it, and the consumer can always
// ask explicitly.
func (r Runner) primeiraExecucao(ctx context.Context, slug, nodeID string) bool {
	if r.Historico == nil {
		return false
	}
	jaTeve, err := r.Historico.PassoJaTeveSucesso(ctx, slug, nodeID, r.RunID)
	if err != nil {
		// A failed query must not turn into a table created by mistake.
		return false
	}
	return !jaTeve
}

// montar escolhe o executor e monta a task.
func (r Runner) montar(w wf.Workflow, n wf.Node, tentativa int, primeira bool) (execution.Executor, execution.TaskExec, error) {
	imagem := w.ImagemDe(n)
	recursos := w.RecursosDe(n)

	t := execution.TaskExec{
		ExecutionID: w.Slug + ":" + n.ID,
		NodeID:      n.ID,
		Workflow:    w.Slug,
		RunID:       r.RunID.String(),
		// The attempt goes into the pod's NAME. Without it, a retry finds the
		// previous attempt's pod again -- and because the executor adopts an
		// existing pod (so it does not start two identical ones when the
		// process dies midway), it stays stuck on the broken pod forever. That
		// is what happened in dev: a pod Pending on insufficient CPU was
		// re-adopted on every retry.
		Tentativa:  tentativa,
		Image:      imagem,
		Shell:      n.UsaShell(),
		CPU:        recursos.CPU,
		Memoria:    recursos.Memory,
		CPUMax:     recursos.CPULimit,
		MemoriaMax: recursos.MemoryLimit,
		WorkDir:    r.WorkDir,
		// Order, weakest to strongest: run context, the engine's global
		// environment, the workflow's `env:`, the step's `env:`. The step
		// beats the global on purpose -- the other way round, a variable
		// declared in the file would lose in silence to a BREVIS_TASK_ENV
		// somebody configured months ago.
		Env:     mesclarEnv(r.Env, r.contextoDoRun(n.ID, primeira, tentativa), w.EnvDe(n)),
		Secrets: w.SecretsDe(n),
		Timeout: r.Timeout,
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
	comando, err := execution.Renderizar(n.Run, r.Params)
	if err != nil {
		return nil, t, fmt.Errorf("step %q: %w", n.ID, err)
	}
	t.Command = comando

	// A step with `image:` runs as a POD when a pod executor exists. That is the
	// difference between local mode and the cluster, and it lives HERE, in one
	// place -- the YAML is identical in both, and neither executor knows the
	// other exists.
	if imagem != "" && r.Pods != nil {
		return r.Pods, t, nil
	}
	if r.Processo == nil {
		if imagem != "" {
			return nil, t, fmt.Errorf("step %q declara `image: %s`, mas este processo nao tem executor de pods nem de processo", n.ID, imagem)
		}
		return nil, t, fmt.Errorf("step %q usa `run:`, mas nenhum executor de processo foi configurado", n.ID)
	}
	// Local with an `image:` declared: runs on the instance itself and WARNS.
	// Staying quiet would make it look as though the step ran in the declared
	// image, which is the kind of mistake that only surfaces once the result is
	// already wrong.
	if imagem != "" && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
			Message: fmt.Sprintf("modo local: rodando na instancia, ignorando `image: %s`", imagem),
		})
	}
	return r.Processo, t, nil
}
