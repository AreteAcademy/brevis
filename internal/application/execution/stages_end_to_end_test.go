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
type sdkSpeaker struct{ lines []string }

func (sdkSpeaker) Name() string                         { return "falador" }
func (sdkSpeaker) Cancel(context.Context, string) error { return nil }

func (f sdkSpeaker) Execute(context.Context, execution.TaskExec) (<-chan execution.Event, error) {
	ch := make(chan execution.Event, len(f.lines)+2)
	ch <- execution.Event{Kind: execution.EventStarted}
	for _, l := range f.lines {
		ch <- execution.Event{Kind: execution.EventLog, NodeID: "collectOutput", Stream: "stdout", Message: l}
	}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch, nil
}

// spyPersister keeps what the runner asked to be written.
type spyPersister struct {
	stages  json.RawMessage
	version string
	log     string
	called  int
}

func (p *spyPersister) IniciarTask(context.Context, uuid.UUID, string, int) error { return nil }

func (p *spyPersister) TerminarTask(_ context.Context, _ uuid.UUID, _ string, _ int,
	_ dom.Status, _ *int, _ string, log string) error {
	p.log = log
	return nil
}

func (p *spyPersister) RecordStages(_ context.Context, _ uuid.UUID, _ string, _ int,
	version string, stages json.RawMessage) error {
	p.called++
	p.version, p.stages = version, stages
	return nil
}

// spyReporter keeps what would reach the CLI's screen.
type spyReporter struct{ lines []string }

func (r *spyReporter) Evento(e execution.Event) {
	if e.Kind == execution.EventLog {
		r.lines = append(r.lines, e.Message)
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
	spy := &spyPersister{}
	tela := &spyReporter{}
	r := app.Runner{
		RunID:   uuid.New(),
		Persist: spy,
		Report:  tela,
		Processo: sdkSpeaker{lines: []string{
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

	if spy.version != "v0.44.1" {
		t.Errorf("the version never reached the database: %q", spy.version)
	}
	var stages []app.RecordedStage
	if err := json.Unmarshal(spy.stages, &stages); err != nil {
		t.Fatalf("etapas ilegiveis: %v — %s", err, spy.stages)
	}
	if len(stages) != 1 || stages[0].TaskName != "extract" || stages[0].State != "done" {
		t.Fatalf("etapas: %+v", stages)
	}

	// The marker must NOT become a log line: whoever is watching wants the
	// stages, not the JSON that
	// as transportou.
	if strings.Contains(spy.log, "@brevis:") {
		t.Errorf("the marked line ended up in the step's log:\n%s", spy.log)
	}
	// Nem na tela do CLI.
	for _, l := range tela.lines {
		if strings.Contains(l, "@brevis:") {
			t.Errorf("the marked line went to the Report: %q", l)
		}
	}
	// E a saida de verdade continua inteira.
	for _, esperada := range []string{"buscando a pagina 1", "pronto"} {
		if !strings.Contains(spy.log, esperada) {
			t.Errorf("a saida do programa se perdeu junto: %q sumiu de\n%s", esperada, spy.log)
		}
	}
}

// A step that is not an SDK one records no stage at all -- and does not pay a
// round trip to the database per log line.
func TestAPlainStepRecordsNoStages(t *testing.T) {
	spy := &spyPersister{}
	r := app.Runner{
		RunID:    uuid.New(),
		Persist:  spy,
		Processo: sdkSpeaker{lines: []string{"compilando", "pronto"}},
	}
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatal(err)
	}
	if spy.called != 0 {
		t.Errorf("it wrote stages %d times for a step that is not an SDK one", spy.called)
	}
}
