package sdk

import (
	"encoding/json"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// O SDK conta ao motor em que etapa esta, por uma linha marcada em stdout.
//
// O cano ja existe e ja esta correndo: o executor acompanha o log do pod
// enquanto o container vive, e o runner ve toda linha, uma a uma. Nao e preciso
// callback, porta nova, autenticacao nova nem RBAC novo -- e o executor local
// ganha o mesmo de graca, porque quem reconhece a marca e o runner, que nao
// sabe qual executor produziu o evento.
//
// Uma linha por transicao. O `copiar` do executor quebra por linha, entao um
// evento que nao coubesse numa linha simplesmente nao chegaria.
const marcaEtapa = "@brevis:"

// As etapas que o pipeline anuncia. Lista fechada de proposito: uma etapa
// desconhecida e ignorada pelo motor em vez de inventar bloco na tela.
const (
	EtapaCheck     = "check"
	EtapaExtract   = "extract"
	EtapaTransform = "transform"
	EtapaLoad      = "load"
)

// Estados de uma etapa. `aborted` nao e emitido daqui: quem o decide e o motor,
// ao ver o passo terminar com uma etapa ainda em running -- porque um processo
// que morreu nao emite nada.
const (
	EstadoRodando = "running"
	EstadoPronto  = "done"
	EstadoFalhou  = "failed"
)

// tetoDeEtapas limita quantas transicoes um processo pode anunciar.
//
// O stream de log vira escrita em banco do outro lado. Sem teto, um pipeline em
// laco derrubaria o Postgres pelo caminho do log -- e o log e justamente o que
// nao pode parar de funcionar quando algo esta errado.
const tetoDeEtapas = 50

// saidaDasEtapas e o stdout, e e trocavel para que um teste possa ler o que
// foi anunciado. Mesma costura do slog.SetDefault que os outros testes usam.
var saidaDasEtapas io.Writer = os.Stdout

type relator struct {
	mu     sync.Mutex
	emitiu int
	ligado bool
	inicio map[string]time.Time
}

// novoRelator devolve um relator ligado apenas sob o motor.
//
// Rodando a mao, as linhas nao servem a ninguem e so sujariam o terminal de
// quem esta depurando um fetcher.
func novoRelator(run RunContext) *relator {
	return &relator{
		ligado: run.FromEngine(),
		inicio: map[string]time.Time{},
	}
}

// anunciar diz que este passo e um pipeline do SDK, e com que versao.
//
// A versao sai do proprio binario: ninguem digita, ninguem mantem em
// sincronia, e nao ha como o selo mentir. Um selo errado seria pior que selo
// nenhum, porque ele e o que se olha para descartar hipoteses.
func (r *relator) anunciar(pipeline string) {
	r.emitir(map[string]any{
		"tipo":     "sdk",
		"versao":   VersaoDoSDK(),
		"pipeline": pipeline,
	})
}

func (r *relator) comecou(etapa string) {
	r.mu.Lock()
	r.inicio[etapa] = time.Now()
	r.mu.Unlock()
	r.emitir(map[string]any{"tipo": "etapa", "nome": etapa, "estado": EstadoRodando})
}

// terminou fecha a etapa. Os numeros vao junto porque estado sem numero nao
// diz nada: "extract pronto" e menos util que "extract pronto, 300 paginas,
// 48.213 linhas".
// semRelogio sao as etapas que nao reportam duracao.
//
// O transform roda por registro, entremeado com a leitura, entao qualquer
// numero que saisse dali seria o tempo de outra coisa -- na pratica o da
// extracao, que e quem dita o ritmo do fluxo. Um numero ausente e melhor que um
// numero errado, e um `transform: 40min` ao lado de `extract: 40min` faria
// alguem procurar o gargalo no lugar errado.
var semRelogio = map[string]bool{EtapaTransform: true}

func (r *relator) terminou(etapa, estado string, numeros map[string]any) {
	r.mu.Lock()
	desde, tinha := r.inicio[etapa]
	r.mu.Unlock()

	ev := map[string]any{"tipo": "etapa", "nome": etapa, "estado": estado}
	if tinha && !semRelogio[etapa] {
		ev["ms"] = time.Since(desde).Milliseconds()
	}
	for k, v := range numeros {
		ev[k] = v
	}
	r.emitir(ev)
}

func (r *relator) emitir(ev map[string]any) {
	if r == nil || !r.ligado {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.emitiu >= tetoDeEtapas {
		return
	}
	r.emitiu++

	ev["em"] = time.Now().UTC().Format(time.RFC3339Nano)
	linha, err := json.Marshal(ev)
	if err != nil {
		return // um evento que nao serializa nao vale derrubar o pipeline
	}
	// Ignorado de proposito: se o stdout nao aceita mais escrita, o pipeline
	// tem problema maior, e falhar por causa da telemetria seria trocar uma
	// tela incompleta por uma execucao perdida.
	_, _ = io.WriteString(saidaDasEtapas, marcaEtapa+string(linha)+"\n")
}

// VersaoDoSDK e a versao deste modulo, lida do proprio binario.
//
// Devolve "devel" quando o fetcher foi compilado a partir de um checkout ou de
// um replace -- que e a verdade, e melhor que inventar um numero.
func VersaoDoSDK() string {
	versao := func() string {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			return ""
		}
		for _, d := range info.Deps {
			if d.Path == caminhoDoModulo {
				return d.Version
			}
		}
		// O proprio modulo, quando os testes do SDK rodam dentro dele.
		if info.Main.Path == caminhoDoModulo {
			return info.Main.Version
		}
		return ""
	}()

	switch versao {
	case "", "(devel)":
		return "devel"
	default:
		return versao
	}
}

const caminhoDoModulo = "github.com/AreteAcademy/brevis/sdk"

// numerosDoExtract e o que a etapa de extracao produziu.
func numerosDoExtract(d *Data) map[string]any {
	if d == nil {
		return nil
	}
	st := d.Stats()
	n := map[string]any{}
	if st.Pages > 0 {
		n["paginas"] = st.Pages
	}
	if st.Attempts > 0 {
		n["tentativas_http"] = st.Attempts
	}
	return n
}

// numerosDoLoad e o que a carga produziu.
func numerosDoLoad(res *Result) map[string]any {
	if res == nil {
		return nil
	}
	n := map[string]any{"linhas": res.Rows, "registros": res.Records}
	if res.Strategy != "" {
		n["estrategia"] = res.Strategy
	}
	if res.CheckpointReused {
		n["checkpoint"] = "reaproveitado"
	}
	if len(res.Objects) > 0 {
		n["objetos"] = strconv.Itoa(len(res.Objects))
	}
	return n
}
