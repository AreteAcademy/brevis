package execution_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
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
//
// The mutex is not decoration. A level runs its steps in PARALLEL, so two of
// them call into this at the same time -- which the race detector found the
// moment a test with more than one step per level existed.
type spyPersister struct {
	mu      sync.Mutex
	stages  json.RawMessage
	version string
	log     string
	called  int

	// skipped is node -> reason, for the trigger-rule tests.
	skipped map[string]string

	// ended is every instance that reached a terminal state, for the mapping
	// tests: a mapped step's four rows are four keys here.
	ended map[dom.StepKey]dom.Status

	// loads is what each instance reported for the trend table. A step that
	// reported no finished load leaves NO entry, which is what the tests that
	// assert silence look at.
	loads map[dom.StepKey]app.LoadNumbers
}

// instances lists the keys that finished, for a test to count.
func (p *spyPersister) instances() map[dom.StepKey]dom.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[dom.StepKey]dom.Status, len(p.ended))
	for k, v := range p.ended {
		out[k] = v
	}
	return out
}

func (p *spyPersister) IniciarTask(context.Context, uuid.UUID, dom.StepKey, int) error { return nil }

func (p *spyPersister) TerminarTask(_ context.Context, _ uuid.UUID, step dom.StepKey, _ int,
	status dom.Status, _ *int, _ string, log string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = log
	if p.ended == nil {
		p.ended = map[dom.StepKey]dom.Status{}
	}
	p.ended[step] = status
	return nil
}

func (p *spyPersister) RecordStages(_ context.Context, _ uuid.UUID, _ dom.StepKey, _ int,
	version string, stages json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.called++
	p.version, p.stages = version, stages
	return nil
}

func (p *spyPersister) RecordLoad(_ context.Context, _ uuid.UUID, step dom.StepKey,
	_ string, n app.LoadNumbers) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loads == nil {
		p.loads = map[dom.StepKey]app.LoadNumbers{}
	}
	p.loads[step] = n
	return nil
}

// loaded lists what reached the trend table, for a test to count.
func (p *spyPersister) loaded() map[dom.StepKey]app.LoadNumbers {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[dom.StepKey]app.LoadNumbers, len(p.loads))
	for k, v := range p.loads {
		out[k] = v
	}
	return out
}

func (p *spyPersister) MarkSkipped(_ context.Context, _ uuid.UUID, step dom.StepKey,
	_ int, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.skipped == nil {
		p.skipped = map[string]string{}
	}
	p.skipped[step.Node] = reason
	return nil
}

// skips is a copy, for a test to read after the run.
func (p *spyPersister) skips() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.skipped))
	for k, v := range p.skipped {
		out[k] = v
	}
	return out
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
// executor gets the same for free.
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

// The trend row reaches the database through the same pipe as everything else:
// the step writes marked lines, the executor delivers them as log, the runner
// flattens the phases into one row.
//
// Written ONCE, at the end. markStages runs on every marked line because the
// screen has to advance while the step is alive, and a trend row rewritten per
// transition would be a database round trip each time to store a number that is
// not final yet -- so this asserts the count, not just the content.
func TestTheLoadRowReachesTheDatabaseOnceThroughTheStepsLog(t *testing.T) {
	spy := &spyPersister{}
	r := app.Runner{
		RunID:   uuid.New(),
		Persist: spy,
		Report:  &spyReporter{},
		Processo: sdkSpeaker{lines: []string{
			`@brevis:{"type":"sdk","version":"v0.58.0","pipeline":"vendas"}`,
			`@brevis:{"type":"stage","index":0,"name":"extract","state":"running","at":"agora"}`,
			`@brevis:{"type":"stage","index":0,"name":"extract","state":"done","at":"agora","ms":31000,"pages":12,"bytes":5000000,"http_attempts":13}`,
			`@brevis:{"type":"stage","index":1,"name":"load","state":"running","at":"agora"}`,
			`@brevis:{"type":"stage","index":1,"name":"load","state":"done","at":"agora","ms":22000,"rows":48213,"records":48300,"load_bytes":900000}`,
		}},
	}
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("run: %v", err)
	}

	loaded := spy.loaded()
	if len(loaded) != 1 {
		t.Fatalf("the trend got %d rows, wanted exactly 1: %+v", len(loaded), loaded)
	}
	for _, n := range loaded {
		want := app.LoadNumbers{
			Rows: 48213, Records: 48300, BytesOut: 900000, LoadMs: 22000,
			BytesIn: 5000000, Pages: 12, HTTPAttempts: 13, ExtractMs: 31000,
		}
		if n != want {
			t.Errorf("got  %+v\nwant %+v", n, want)
		}
	}
}

// And the step that is NOT a pipeline writes nothing.
//
// This is the case that matters for the numbers on the screen: most steps are a
// shell command, and a row of zeros from each of them would drag every average
// the trend draws toward a floor that never happened.
func TestAStepWithNoLoadWritesNoTrendRow(t *testing.T) {
	for name, lines := range map[string][]string{
		"a plain command": {"copiando arquivos", "pronto"},
		"an SDK step whose load failed": {
			`@brevis:{"type":"stage","index":0,"name":"extract","state":"done","at":"x","ms":900}`,
			`@brevis:{"type":"stage","index":1,"name":"load","state":"failed","at":"x","rows":40000}`,
		},
		"an SDK step that died mid-load": {
			`@brevis:{"type":"stage","index":1,"name":"load","state":"running","at":"x"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			spy := &spyPersister{}
			r := app.Runner{
				RunID: uuid.New(), Persist: spy, Report: &spyReporter{},
				Processo: sdkSpeaker{lines: lines},
			}
			if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := spy.loaded(); len(got) != 0 {
				t.Errorf("the trend got a row it should not have: %+v", got)
			}
		})
	}
}
