package kubernetes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// API is what the executor needs from the server. An interface in the consumer:
// it is what makes it possible to test the pod's whole lifecycle against a fake
// server.
type API interface {
	CriarPod(ctx context.Context, p Pod) (Pod, error)
	LerPod(ctx context.Context, nome string) (Pod, error)
	Logs(ctx context.Context, nome string, seguir bool) (io.ReadCloser, error)
	ApagarPod(ctx context.Context, nome string) error
}

// Executor runs each step as a pod.
//
// The cycle is always the same: create the pod, wait for it to leave Pending,
// follow the log while it runs, read the exit code and delete it. No state lives
// here beyond the in-flight pods -- if the process restarts, the pods keep
// running and the dispatcher finds them again by their deterministic name.
type Executor struct {
	api  API
	opts Opcoes

	// Status polling interval. Polling rather than watching is deliberate: a
	// watch needs reconnection, resync and handling of missed events to gain
	// seconds on a task that lasts minutes.
	Intervalo time.Duration

	mu    sync.Mutex
	emVoo map[string]string // execID -> nome do pod
}

func NewExecutor(api API, o Opcoes) *Executor {
	return &Executor{
		api: api, opts: o.comPadroes(),
		Intervalo: time.Second,
		emVoo:     map[string]string{},
	}
}

func (e *Executor) Name() string { return "kubernetes" }

// Execute creates the pod and returns the event channel. The channel closes when
// the pod finishes -- the same shape as the local executor, so the runner cannot
// tell them apart.
func (e *Executor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	spec, err := MontarPod(t, e.opts)
	if err != nil {
		return nil, err
	}

	criado, err := e.api.CriarPod(ctx, spec)
	if err != nil {
		// AlreadyExists is not an error: the name is deterministic per attempt,
		// so this means an earlier run created the pod and died before
		// recording it. Adopting the existing pod avoids running the same dbt
		// twice in parallel.
		if !strings.Contains(err.Error(), "already exists") {
			return nil, err
		}
		criado = spec
	}
	nome := criado.Metadata.Name

	e.mu.Lock()
	e.emVoo[t.ExecutionID] = nome
	e.mu.Unlock()

	eventos := make(chan execution.Event, 64)
	go func() {
		defer close(eventos)
		defer func() {
			e.mu.Lock()
			delete(e.emVoo, t.ExecutionID)
			e.mu.Unlock()
		}()
		e.acompanhar(ctx, nome, t, eventos)
	}()
	return eventos, nil
}

func (e *Executor) acompanhar(ctx context.Context, nome string, t execution.TaskExec, eventos chan<- execution.Event) {
	eventos <- execution.Event{
		Kind: execution.EventStarted, NodeID: t.NodeID,
		Message: fmt.Sprintf("pod %s (%s)", nome, t.Image),
	}

	pod, err := e.esperarSair(ctx, nome, t, eventos)
	if err != nil {
		eventos <- execution.Event{
			Kind: execution.EventFailed, NodeID: t.NodeID,
			Message: err.Error(), Err: err,
		}
		e.limpar(nome, false)
		return
	}

	// The log is drained to the end BEFORE reporting the outcome: closing the
	// channel with lines still buffered would lose precisely the last ones,
	// which are the ones that explain the failure.
	e.drenarLogs(ctx, nome, t, eventos)

	codigo, terminou := pod.Saida()
	if pod.Fase() == "Succeeded" {
		eventos <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID, ExitCode: codigo}
		e.limpar(nome, true)
		return
	}

	msg := fmt.Sprintf("pod %s terminou em %s", nome, pod.Fase())
	if terminou {
		msg = fmt.Sprintf("saiu com codigo %d", codigo)
	}
	if pod.Motivo() != "" {
		// DeadlineExceeded, OOMKilled, Evicted: the difference between "the code
		// failed" and "the cluster killed the process".
		msg += " (" + pod.Motivo() + ")"
	}
	eventos <- execution.Event{
		Kind: execution.EventFailed, NodeID: t.NodeID,
		ExitCode: codigo, Message: msg, Err: errors.New(msg),
	}
	e.limpar(nome, false)
}

// esperarSair polls until the pod finishes, reporting why it is waiting.
func (e *Executor) esperarSair(ctx context.Context, nome string, t execution.TaskExec,
	eventos chan<- execution.Event) (Pod, error) {

	tick := time.NewTicker(e.Intervalo)
	defer tick.Stop()

	// The log follower writes to the SAME channel the caller closes on the way
	// out. Without waiting for it, `close(eventos)` could fire with a send in
	// flight -- `send on closed channel`, which brings down the whole process
	// and not just the run. The -race detector found this the first time the
	// root module was tested in CI.
	//
	// Cancelling before waiting is what bounds the wait: the log response's
	// body closes with the context, so the follower exits. Losing the rest of
	// the live follow costs nothing -- drenarLogs reads the whole log right
	// afterwards, which is how the last lines already arrived.
	ctxLogs, pararLogs := context.WithCancel(ctx)
	var seguidores sync.WaitGroup
	defer func() {
		pararLogs()
		seguidores.Wait()
	}()

	var ultimoMotivo string
	seguindo := false
	comecou := time.Now()

	for {
		pod, err := e.api.LerPod(ctx, nome)
		if err != nil {
			return Pod{}, fmt.Errorf("lendo pod %s: %w", nome, err)
		}
		if pod.Terminou() {
			return pod, nil
		}

		// A pod stuck in ImagePullBackOff or CreateContainerConfigError produces
		// no log at all: without reporting the reason, the step would look
		// jammed until the timeout, with not a line explaining it.
		if motivo := pod.MotivoDeEspera(); motivo != "" && motivo != ultimoMotivo {
			ultimoMotivo = motivo
			eventos <- execution.Event{
				Kind: execution.EventLog, NodeID: t.NodeID, Stream: "stderr",
				Message: "pod aguardando: " + motivo,
			}
		}

		// As soon as the container runs, the log is followed in parallel -- the
		// operator sees dbt's output live rather than only at the end.
		if !seguindo && pod.Fase() == "Running" {
			seguindo = true
			seguidores.Add(1)
			go func() {
				defer seguidores.Done()
				e.seguirLogs(ctxLogs, nome, t, eventos)
			}()
		}

		// A pod that never leaves Pending is not an error to Kubernetes: it
		// waits forever for a node it fits on. Without this cut-off the step
		// waits with it -- no failure and no retry -- which is how a CPU request
		// larger than the pool's free capacity jammed an entire run in dev.
		if !seguindo && time.Since(comecou) > e.opts.EsperaParaIniciar {
			motivo := pod.MotivoDeEspera()
			if motivo == "" {
				motivo = "fase " + pod.Fase()
			}
			if agendamento := e.porQueNaoAgendou(ctx, nome); agendamento != "" {
				motivo = agendamento
			}
			return Pod{}, fmt.Errorf("pod %s nao comecou em %s: %s",
				nome, e.opts.EsperaParaIniciar, motivo)
		}

		select {
		case <-ctx.Done():
			return Pod{}, ctx.Err()
		case <-tick.C:
		}
	}
}

// porQueNaoAgendou reads the PodScheduled condition, which is where the
// scheduler explains "Insufficient cpu" or "didn't match node affinity".
// Without it the message would say only "Pending", which helps nobody.
func (e *Executor) porQueNaoAgendou(ctx context.Context, nome string) string {
	pod, err := e.api.LerPod(ctx, nome)
	if err != nil || pod.Status == nil {
		return ""
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status != "True" && c.Message != "" {
			return c.Reason + ": " + c.Message
		}
	}
	return ""
}

// seguirLogs follows the output while the container lives.
func (e *Executor) seguirLogs(ctx context.Context, nome string, t execution.TaskExec, eventos chan<- execution.Event) {
	corpo, err := e.api.Logs(ctx, nome, true)
	if err != nil {
		return // the log may not be ready; drenarLogs still reads it at the end
	}
	defer func() { _ = corpo.Close() }()
	copiar(corpo, t.NodeID, eventos)
}

// drenarLogs reads the complete output once the pod has finished.
//
// Without following: the container is over, and `follow` on a closed output only
// returns the same content. The repeated lines from the stretch already streamed
// are the price of not losing the end -- and losing the end is what stops anyone
// understanding the failure.
func (e *Executor) drenarLogs(ctx context.Context, nome string, t execution.TaskExec, eventos chan<- execution.Event) {
	corpo, err := e.api.Logs(ctx, nome, false)
	if err != nil {
		eventos <- execution.Event{
			Kind: execution.EventLog, NodeID: t.NodeID, Stream: "stderr",
			Message: "nao consegui ler o log do pod: " + err.Error(),
		}
		return
	}
	defer func() { _ = corpo.Close() }()
	copiar(corpo, t.NodeID, eventos)
}

func copiar(r io.Reader, nodeID string, eventos chan<- execution.Event) {
	s := bufio.NewScanner(r)
	// A dbt line carrying SQL can exceed 64 KB, the Scanner's default limit --
	// and a Scanner that overflows stops reading in silence.
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		// The pod's log arrives on a single stream: Kubernetes does not separate
		// stdout from stderr. Marking everything as stdout is a smaller lie than
		// the opposite, but the origin information simply does not exist here.
		eventos <- execution.Event{
			Kind: execution.EventLog, NodeID: nodeID,
			Stream: "stdout", Message: s.Text(),
		}
	}
}

// limpar deletes the pod, honouring the option to keep the failed ones.
func (e *Executor) limpar(nome string, sucesso bool) {
	if !sucesso && e.opts.ManterPodEmFalha {
		return
	}
	// A context of its own: the run's may already be cancelled (the
	// cancellation is what brought us here), and deleting is precisely what
	// must not be skipped -- an orphaned pod consumes namespace quota
	// forever.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = e.api.ApagarPod(ctx, nome)
}

// Cancel apaga o pod da execucao em voo.
func (e *Executor) Cancel(ctx context.Context, execID string) error {
	e.mu.Lock()
	nome, ok := e.emVoo[execID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("execucao %q nao esta rodando", execID)
	}
	return e.api.ApagarPod(ctx, nome)
}
