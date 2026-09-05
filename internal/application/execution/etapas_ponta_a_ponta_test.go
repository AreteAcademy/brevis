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

// faladorDoSDK e um passo que fala como o SDK fala: uma linha marcada por
// transicao, no meio da saida normal.
type faladorDoSDK struct{ linhas []string }

func (faladorDoSDK) Name() string                         { return "falador" }
func (faladorDoSDK) Cancel(context.Context, string) error { return nil }

func (f faladorDoSDK) Execute(context.Context, execution.TaskExec) (<-chan execution.Event, error) {
	ch := make(chan execution.Event, len(f.linhas)+2)
	ch <- execution.Event{Kind: execution.EventStarted}
	for _, l := range f.linhas {
		ch <- execution.Event{Kind: execution.EventLog, NodeID: "coletar", Stream: "stdout", Message: l}
	}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch, nil
}

// persistidorEspiao guarda o que o runner mandou gravar.
type persistidorEspiao struct {
	etapas json.RawMessage
	versao string
	log    string
	chamou int
}

func (p *persistidorEspiao) IniciarTask(context.Context, uuid.UUID, string, int) error { return nil }

func (p *persistidorEspiao) TerminarTask(_ context.Context, _ uuid.UUID, _ string, _ int,
	_ dom.Status, _ *int, _ string, log string) error {
	p.log = log
	return nil
}

func (p *persistidorEspiao) RegistrarEtapas(_ context.Context, _ uuid.UUID, _ string, _ int,
	versao string, etapas json.RawMessage) error {
	p.chamou++
	p.versao, p.etapas = versao, etapas
	return nil
}

// relatorEspiao guarda o que chegaria a tela do CLI.
type relatorEspiao struct{ linhas []string }

func (r *relatorEspiao) Evento(e execution.Event) {
	if e.Kind == execution.EventLog {
		r.linhas = append(r.linhas, e.Message)
	}
}

// O cano inteiro: o passo escreve linhas marcadas no stdout, o executor as
// entrega como log, e o runner as transforma em etapas -- sem callback, sem
// porta nova, sem RBAC novo.
//
// E o executor aqui e um fake qualquer: quem reconhece a marca e o runner, que
// nao sabe qual executor produziu o evento. E por isso que o executor LOCAL
// ganha o mesmo de graca.
func TestEtapasChegamPeloLogDoPasso(t *testing.T) {
	espiao := &persistidorEspiao{}
	tela := &relatorEspiao{}
	r := app.Runner{
		RunID:   uuid.New(),
		Persist: espiao,
		Report:  tela,
		Processo: faladorDoSDK{linhas: []string{
			`@brevis:{"tipo":"sdk","versao":"v0.44.1","pipeline":"clima"}`,
			"buscando a pagina 1",
			`@brevis:{"tipo":"etapa","nome":"extract","estado":"running","em":"agora"}`,
			`@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"paginas":300}`,
			"pronto",
		}},
	}
	if err := r.Run(context.Background(), workflowDeUmPasso()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if espiao.versao != "v0.44.1" {
		t.Errorf("a versao nao chegou ao banco: %q", espiao.versao)
	}
	var etapas []app.EtapaGravada
	if err := json.Unmarshal(espiao.etapas, &etapas); err != nil {
		t.Fatalf("etapas ilegiveis: %v — %s", err, espiao.etapas)
	}
	if len(etapas) != 1 || etapas[0].Nome != "extract" || etapas[0].Estado != "done" {
		t.Fatalf("etapas: %+v", etapas)
	}

	// A marca NAO pode virar log: quem olha quer ver as etapas, nao o JSON que
	// as transportou.
	if strings.Contains(espiao.log, "@brevis:") {
		t.Errorf("a linha marcada foi parar no log do passo:\n%s", espiao.log)
	}
	// Nem na tela do CLI.
	for _, l := range tela.linhas {
		if strings.Contains(l, "@brevis:") {
			t.Errorf("a linha marcada foi para o Report: %q", l)
		}
	}
	// E a saida de verdade continua inteira.
	for _, esperada := range []string{"buscando a pagina 1", "pronto"} {
		if !strings.Contains(espiao.log, esperada) {
			t.Errorf("a saida do programa se perdeu junto: %q sumiu de\n%s", esperada, espiao.log)
		}
	}
}

// Um passo que nao e do SDK nao registra etapa nenhuma -- e nao paga uma ida
// ao banco por linha de log.
func TestPassoComumNaoRegistraEtapas(t *testing.T) {
	espiao := &persistidorEspiao{}
	r := app.Runner{
		RunID:    uuid.New(),
		Persist:  espiao,
		Processo: faladorDoSDK{linhas: []string{"compilando", "pronto"}},
	}
	if err := r.Run(context.Background(), workflowDeUmPasso()); err != nil {
		t.Fatal(err)
	}
	if espiao.chamou != 0 {
		t.Errorf("gravou etapas %d vezes para um passo que nao e do SDK", espiao.chamou)
	}
}
