package kubernetes_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
	k8s "github.com/AreteAcademy/brevis/internal/execution/kubernetes"
)

// fakeAPI stands in for the API server: a sequence of phases, the log and a
// record of what was asked. Testing the whole cycle with no cluster is what makes
// this
// caminho verificavel na CI.
type apiFalsa struct {
	mu sync.Mutex

	fases     []k8s.Pod // devolvidas em ordem, a ultima repete
	lidas     int
	log       string
	erroLog   error
	erroCriar error

	criados  []k8s.Pod
	apagados []string
}

func (a *apiFalsa) CriarPod(_ context.Context, p k8s.Pod) (k8s.Pod, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.erroCriar != nil {
		return k8s.Pod{}, a.erroCriar
	}
	a.criados = append(a.criados, p)
	return p, nil
}

func (a *apiFalsa) LerPod(_ context.Context, _ string) (k8s.Pod, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.lidas
	if i >= len(a.fases) {
		i = len(a.fases) - 1
	}
	a.lidas++
	return a.fases[i], nil
}

func (a *apiFalsa) Logs(_ context.Context, _ string, _ bool) (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.erroLog != nil {
		return nil, a.erroLog
	}
	return io.NopCloser(strings.NewReader(a.log)), nil
}

func (a *apiFalsa) ApagarPod(_ context.Context, nome string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.apagados = append(a.apagados, nome)
	return nil
}

func phase(f string) k8s.Pod {
	return k8s.Pod{
		Metadata: k8s.Metadata{Name: "p"},
		Status:   &k8s.PodStatus{Phase: f},
	}
}

func withOutput(f string, codigo int) k8s.Pod {
	p := phase(f)
	var st k8s.StatusContainer
	st.Name = "step"
	st.State.Terminated = &struct {
		ExitCode int    `json:"exitCode"`
		Reason   string `json:"reason"`
		Message  string `json:"message"`
	}{ExitCode: codigo}
	p.Status.ContainerStatuses = []k8s.StatusContainer{st}
	return p
}

func runStep(t *testing.T, api *apiFalsa, tk execution.TaskExec) []execution.Event {
	t.Helper()
	e := k8s.NewExecutor(api, k8s.Opcoes{Namespace: "dados"})
	e.Intervalo = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := e.Execute(ctx, tk)
	if err != nil {
		t.Fatal(err)
	}
	var out []execution.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestASuccessfulPodReportsTheLogAndIsDeleted(t *testing.T) {
	api := &apiFalsa{
		fases: []k8s.Pod{phase("Pending"), phase("Running"), withOutput("Succeeded", 0)},
		log:   "Running with dbt=1.10.3\nCompleted successfully\n",
	}
	eventos := runStep(t, api, task())

	var sucesso bool
	var linhas []string
	for _, e := range eventos {
		switch e.Kind {
		case execution.EventSucceeded:
			sucesso = true
		case execution.EventLog:
			linhas = append(linhas, e.Message)
		}
	}
	if !sucesso {
		t.Fatalf("no success event: %+v", eventos)
	}
	if len(linhas) == 0 || !strings.Contains(strings.Join(linhas, "\n"), "Completed successfully") {
		t.Errorf("the pod's log did not arrive: %v", linhas)
	}
	if len(api.apagados) != 1 {
		t.Errorf("a successful pod has to be deleted (deleted=%v)", api.apagados)
	}
	if len(api.criados) != 1 || api.criados[0].Spec.Containers[0].Image != task().Image {
		t.Errorf("pod criado errado: %+v", api.criados)
	}
}

func TestAFailingPodCarriesTheExitCode(t *testing.T) {
	api := &apiFalsa{
		fases: []k8s.Pod{phase("Running"), withOutput("Failed", 2)},
		log:   "Database Error in model x\n",
	}
	eventos := runStep(t, api, task())

	var falha *execution.Event
	for i := range eventos {
		if eventos[i].Kind == execution.EventFailed {
			falha = &eventos[i]
		}
	}
	if falha == nil {
		t.Fatalf("no failure event: %+v", eventos)
	}
	if falha.ExitCode != 2 {
		t.Errorf("exit code = %d, want 2", falha.ExitCode)
	}
	if !strings.Contains(falha.Message, "codigo 2") {
		t.Errorf("mensagem = %q", falha.Message)
	}
}

// O motivo do Kubernetes distingue "o codigo falhou" de "o cluster matou o
// processo" — OOMKilled e DeadlineExceeded exigem acoes opostas.
func TestTheClustersReasonShowsInTheFailure(t *testing.T) {
	morto := withOutput("Failed", 137)
	morto.Status.Reason = "DeadlineExceeded"
	api := &apiFalsa{fases: []k8s.Pod{phase("Running"), morto}}

	for _, e := range runStep(t, api, task()) {
		if e.Kind == execution.EventFailed {
			if !strings.Contains(e.Message, "DeadlineExceeded") {
				t.Errorf("the message has no reason from the cluster: %q", e.Message)
			}
			return
		}
	}
	t.Fatal("no failure event")
}

// A pod stuck in ImagePullBackOff produces no log at all: without reporting the
// reason, the step would look hung until the timeout, with no line explaining.
func TestTheReasonForWaitingIsReported(t *testing.T) {
	preso := phase("Pending")
	var st k8s.StatusContainer
	st.Name = "step"
	st.State.Waiting = &struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}{Reason: "ImagePullBackOff", Message: "Back-off pulling image"}
	preso.Status.ContainerStatuses = []k8s.StatusContainer{st}

	api := &apiFalsa{fases: []k8s.Pod{preso, preso, withOutput("Failed", 1)}}

	var achou bool
	for _, e := range runStep(t, api, task()) {
		if e.Kind == execution.EventLog && strings.Contains(e.Message, "ImagePullBackOff") {
			achou = true
		}
	}
	if !achou {
		t.Error("the reason for waiting was not reported -- the step would look hung with no explanation")
	}
}

// A deterministic name: a pod that already exists was created by a run that
// morreu antes de registrar. Adotar evita subir um segundo rodando o mesmo dbt.
func TestAnAlreadyExistingPodIsAdopted(t *testing.T) {
	api := &apiFalsa{
		erroCriar: errors.New(`pods "x" already exists`),
		fases:     []k8s.Pod{withOutput("Succeeded", 0)},
	}
	eventos := runStep(t, api, task())
	for _, e := range eventos {
		if e.Kind == execution.EventSucceeded {
			return
		}
	}
	t.Fatalf("an existing pod should be followed, not refused: %+v", eventos)
}

// A creation failure (RBAC, quota, an invalid image) has to surface as the step's
// error, rather than become a ghost pod nobody follows.
func TestACreationErrorReachesTheCaller(t *testing.T) {
	api := &apiFalsa{erroCriar: errors.New(`pods is forbidden: cannot create resource "pods"`)}
	e := k8s.NewExecutor(api, k8s.Opcoes{})
	if _, err := e.Execute(context.Background(), task()); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("the error lost the cause: %v", err)
	}
}

func TestAFailedPodMayBeKeptForInspection(t *testing.T) {
	api := &apiFalsa{fases: []k8s.Pod{withOutput("Failed", 1)}}
	e := k8s.NewExecutor(api, k8s.Opcoes{ManterPodEmFalha: true})
	e.Intervalo = time.Millisecond

	ch, err := e.Execute(context.Background(), task())
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if len(api.apagados) != 0 {
		t.Errorf("a failed pod was deleted despite the option: %v", api.apagados)
	}
}

// `Pending` is not an error to Kubernetes: a pod that fits on no node stays there
// forever. Without a cutoff the stage waits along with it -- no failure and no
// retry -- which is how a CPU request larger than the pool's free capacity hung a
// run
// inteira em dev.
func TestAPodThatDoesNotStartFailsWithTheSchedulersReason(t *testing.T) {
	preso := phase("Pending")
	preso.Status.Conditions = []k8s.Condicao{{
		Type: "PodScheduled", Status: "False", Reason: "Unschedulable",
		Message: "0/8 nodes are available: 6 Insufficient cpu.",
	}}
	api := &apiFalsa{fases: []k8s.Pod{preso}}

	e := k8s.NewExecutor(api, k8s.Opcoes{EsperaParaIniciar: 30 * time.Millisecond})
	e.Intervalo = time.Millisecond

	ch, err := e.Execute(context.Background(), task())
	if err != nil {
		t.Fatal(err)
	}
	var falha *execution.Event
	for ev := range ch {
		if ev.Kind == execution.EventFailed {
			e := ev
			falha = &e
		}
	}
	if falha == nil {
		t.Fatal("the step never failed -- it would stay stuck forever")
	}
	if !strings.Contains(falha.Message, "Insufficient cpu") {
		t.Errorf("the message does not say why it was not scheduled: %q", falha.Message)
	}
}

// The pod's name distinguishes THE RUN's attempt: without that the dispatcher's
// retry (which restarts the run from scratch) finds the previous attempt's pod
// again.
func TestTheNameChangesWithTheRunsAttempt(t *testing.T) {
	a := task()
	b := task()
	b.TentativaDoRun = 1
	if k8s.NomeDoPod(a) == k8s.NomeDoPod(b) {
		t.Error("attempts diferentes do run geraram o mesmo pod")
	}
}
