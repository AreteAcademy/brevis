package execution

import (
	"encoding/json"
	"strings"
)

// marcaDoSDK e o prefixo que o SDK usa para falar com o motor pelo stdout.
//
// O cano ja existia: o executor acompanha o log do pod enquanto o container
// vive, e este laco ve toda linha, uma a uma. Reconhecer aqui -- e nao no
// executor -- faz o executor LOCAL ganhar o mesmo de graca, porque este codigo
// nao sabe qual deles produziu o evento.
const marcaDoSDK = "@brevis:"

// tetoDeEtapas limita quantas transicoes um passo pode registrar.
//
// O stream de log vira escrita em banco aqui. Sem teto, um pipeline em laco
// derrubaria o Postgres pelo caminho do log -- e o log e justamente o que nao
// pode parar de funcionar quando algo esta errado. O SDK ja se limita; este e o
// teto de quem nao confia no que veio pelo cano.
const tetoDeEtapas = 60

// Etapa e uma fase de um passo do SDK, como ela esta agora.
//
// EtapaGravada e o mesmo tipo sob o nome pelo qual ele atravessa o banco: e o
// que um teste de fora do pacote precisa para conferir o que foi gravado.
type Etapa struct {
	Nome    string         `json:"nome"`
	Estado  string         `json:"estado"`
	Ms      *int64         `json:"ms,omitempty"`
	Em      string         `json:"em"`
	Numeros map[string]any `json:"numeros,omitempty"`
}

// etapasConhecidas e uma lista fechada de proposito: uma etapa que este motor
// nao conhece e ignorada, em vez de virar um bloco sem sentido na tela.
// EtapaGravada e o formato em que uma Etapa vai para o JSONB.
type EtapaGravada = Etapa

var etapasConhecidas = map[string]bool{
	"check": true, "extract": true, "transform": true, "load": true,
}

// coletorDeEtapas monta o estado das etapas a partir das linhas marcadas.
//
// Uma etapa e UMA entrada que muda de estado, e nao duas linhas de historico:
// a tela mostra quatro blocos, nao um diario.
type coletorDeEtapas struct {
	Versao string
	Etapas []Etapa
	vistos int
}

// linha consome uma linha de log. Devolve true quando ela era uma marca -- e
// nesse caso ela NAO deve entrar no log do passo: quem olha a tela quer ver
// etapas, nao JSON no console.
func (c *coletorDeEtapas) linha(msg string) bool {
	corpo, ok := strings.CutPrefix(msg, marcaDoSDK)
	if !ok {
		return false
	}

	// DOIS formatos, e o segundo e uma ponte com data de validade.
	//
	// O SDK ate a v0.47.0 falava em portugues: {"tipo":"etapa","nome",
	// "estado","versao","em"}. Da v0.48.0 em diante fala ingles:
	// {"type":"stage","name","state","version","at"}. Um motor que so
	// entendesse o novo faria as etapas de um fetcher antigo sumirem da tela --
	// sem erro, sem log, so a caixa cinza de volta.
	//
	// A ponte sai quando nao houver fetcher em producao abaixo da v0.48.0.
	var ev struct {
		Tipo   string `json:"type"`
		Versao string `json:"version"`
		Nome   string `json:"name"`
		Estado string `json:"state"`
		Ms     *int64 `json:"ms"`
		Em     string `json:"at"`

		TipoPT   string `json:"tipo"`
		VersaoPT string `json:"versao"`
		NomePT   string `json:"nome"`
		EstadoPT string `json:"estado"`
		EmPT     string `json:"em"`
	}
	if err := json.Unmarshal([]byte(corpo), &ev); err != nil {
		// Uma marca ilegivel volta a ser log: esconde-la faria sumir da tela a
		// unica pista de que algo esta escrevendo lixo no lugar errado.
		return false
	}

	// O formato antigo preenche os campos novos, e o resto do codigo so
	// enxerga um formato.
	if ev.Tipo == "" {
		ev.Tipo, ev.Versao = traduzirTipo(ev.TipoPT), ev.VersaoPT
		ev.Nome, ev.Estado, ev.Em = ev.NomePT, ev.EstadoPT, ev.EmPT
	}

	if c.vistos >= tetoDeEtapas {
		return true // consumida, mas nao registrada
	}
	c.vistos++

	switch ev.Tipo {
	case "sdk":
		c.Versao = ev.Versao
		return true
	case "stage":
		if !etapasConhecidas[ev.Nome] {
			return true
		}
		c.aplicar(Etapa{
			Nome: ev.Nome, Estado: ev.Estado, Ms: ev.Ms, Em: ev.Em,
			Numeros: numerosDe(corpo),
		})
		return true
	}
	return true
}

// aplicar substitui a etapa de mesmo nome, mantendo a ordem de chegada.
func (c *coletorDeEtapas) aplicar(e Etapa) {
	for i := range c.Etapas {
		if c.Etapas[i].Nome == e.Nome {
			c.Etapas[i] = e
			return
		}
	}
	c.Etapas = append(c.Etapas, e)
}

// camposReservados sao os que viram colunas proprias da Etapa; o resto do
// objeto e numero que a etapa produziu.
var camposReservados = map[string]bool{
	"type": true, "name": true, "state": true, "ms": true, "at": true, "version": true,
	// Os do formato antigo; ver coletorDeEtapas.linha.
	"tipo": true, "nome": true, "estado": true, "em": true, "versao": true,
}

func numerosDe(corpo string) map[string]any {
	var tudo map[string]any
	if err := json.Unmarshal([]byte(corpo), &tudo); err != nil {
		return nil
	}
	for k := range tudo {
		if camposReservados[k] {
			delete(tudo, k)
		}
	}
	if len(tudo) == 0 {
		return nil
	}
	return tudo
}

// traduzirTipo mapeia o tipo do formato antigo. Ver coletorDeEtapas.linha.
func traduzirTipo(pt string) string {
	if pt == "etapa" {
		return "stage"
	}
	return pt // "sdk" e igual nos dois
}
