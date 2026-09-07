package execution_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// sdkSpeaker is a step that speaks the way the SDK speaks: one marked line
// per
// transicao, no meio da saida normal.
type sdkSpeaker struct{ linhas []string }

func (sdkSpeaker) Name() string                         { return "falador" }
func (sdkSpeaker) Cancel(context.Context, string) error { return nil }

func (f sdkSpeaker) Execute(context.Context, execution.TaskExec) (<-chan execution.Event, error) {
	ch := make(chan execution.Event, len(f.linhas)+2)
	ch <- execution.Event{Kind: execution.EventStarted}
	for _, l := range f.linhas {
		ch <- execution.Event{Kind: execution.EventLog, NodeID: "collectOutput", Stream: "stdout", Message: l}
	}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch, nil
}

// spyPersister keeps what the runner asked to be written.
type spyPersister struct {
	etapas json.RawMessage
	versao string
	log    string
	chamou int
}

func (p *spyPersister) IniciarTask(context.Context, uuid.UUID, string, int) error { return nil }

func (p *spyPersister) TerminarTask(_ context.Context, _ uuid.UUID, _ string, _ int,
	_ dom.Status, _ *int, _ string, log string) error {
	p.log = log
	return nil
}

func (p *spyPersister) RecordStages(_ context.Context, _ uuid.UUID, _ string, _ int,
	versao string, etapas json.RawMessage) error {
	p.chamou++
	p.versao, p.etapas = versao, etapas
	return nil
}

// spyReporter keeps what would reach the CLI's screen.
type spyReporter struct{ linhas []string }

func (r *spyReporter) Evento(e execution.Event) {
	if e.Kind == execution.EventLog {
		r.linhas = append(r.linhas, e.Message)
	}
}

// The whole pipe: the step writes marked lines to stdout, the executor delivers
// them as log, and the runner turns them into stages -- with no callback, no new
// port, no new RBAC.
//
// And the executor here is any fake: what recognizes the marker is the runner,
// which does not know which executor produced the event. That is why the LOCAL
// executor
// ganha o mesmo de graca.
func TestStagesArriveThroughTheStepsLog(t *testing.T) {
	espiao := &spyPersister{}
	tela := &spyReporter{}
	r := app.Runner{
		RunID:   uuid.New(),
		Persist: espiao,
		Report:  tela,
		Processo: sdkSpeaker{linhas: []string{
			`@brevis:{"tipo":"sdk","versao":"v0.44.1","pipeline":"clima"}`,
			"buscando a pagina 1",
			`@brevis:{"tipo":"etapa","nome":"extract","estado":"running","em":"agora"}`,
			`@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"paginas":300}`,
			"pronto",
		}},
	}
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if espiao.versao != "v0.44.1" {
		t.Errorf("the version never reached the database: %q", espiao.versao)
	}
	var etapas []app.RecordedStage
	if err := json.Unmarshal(espiao.etapas, &etapas); err != nil {
		t.Fatalf("etapas ilegiveis: %v — %s", err, espiao.etapas)
	}
	if len(etapas) != 1 || etapas[0].TaskName != "extract" || etapas[0].State != "done" {
		t.Fatalf("etapas: %+v", etapas)
	}

	// The marker must NOT become a log line: whoever is watching wants the
	// stages, not the JSON that
	// as transportou.
	if strings.Contains(espiao.log, "@brevis:") {
		t.Errorf("the marked line ended up in the step's log:\n%s", espiao.log)
	}
	// Nem na tela do CLI.
	for _, l := range tela.linhas {
		if strings.Contains(l, "@brevis:") {
			t.Errorf("the marked line went to the Report: %q", l)
		}
	}
	// E a saida de verdade continua inteira.
	for _, esperada := range []string{"buscando a pagina 1", "pronto"} {
		if !strings.Contains(espiao.log, esperada) {
			t.Errorf("a saida do programa se perdeu junto: %q sumiu de\n%s", esperada, espiao.log)
		}
	}
}

// A step that is not an SDK one records no stage at all -- and does not pay a
// round trip to the database per log line.
func TestAPlainStepRecordsNoStages(t *testing.T) {
	espiao := &spyPersister{}
	r := app.Runner{
		RunID:    uuid.New(),
		Persist:  espiao,
		Processo: sdkSpeaker{linhas: []string{"compilando", "pronto"}},
	}
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatal(err)
	}
	if espiao.chamou != 0 {
		t.Errorf("it wrote stages %d times for a step that is not an SDK one", espiao.chamou)
	}
}
