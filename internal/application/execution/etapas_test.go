package execution

import "testing"

// A linha marcada e conversa do SDK com o motor, nao saida do programa: ela
// vira etapa e SOME do log. Quem olha a tela quer ver as etapas, nao o JSON
// que as transportou.
func TestLinhaMarcadaEConsumida(t *testing.T) {
	var c coletorDeEtapas
	if !c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running","em":"agora"}`) {
		t.Fatal("a marca nao foi reconhecida")
	}
	if len(c.Etapas) != 1 || c.Etapas[0].Nome != "extract" {
		t.Fatalf("etapas: %+v", c.Etapas)
	}
}

// E o que NAO e marca continua sendo log. Engolir uma linha parecida faria
// sumir da tela a saida de um programa que so por acaso escreveu algo igual.
func TestLinhaComumContinuaSendoLog(t *testing.T) {
	var c coletorDeEtapas
	for _, linha := range []string{
		"rodando o extract",
		"@brevis",
		"@brevis:isto nao e json",
		`prefixo @brevis:{"tipo":"etapa","nome":"load","estado":"done"}`,
	} {
		if c.linha(linha) {
			t.Errorf("engoliu uma linha que era log: %q", linha)
		}
	}
	if len(c.Etapas) != 0 {
		t.Errorf("registrou etapa de linha que nao era marca: %+v", c.Etapas)
	}
}

// Uma etapa e UMA entrada que muda de estado, nao duas linhas de historico: a
// tela mostra quatro blocos, nao um diario.
func TestEtapaEUmaEntradaQueMuda(t *testing.T) {
	var c coletorDeEtapas
	c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"paginas":300}`)

	if len(c.Etapas) != 1 {
		t.Fatalf("virou %d entradas, esperado 1: %+v", len(c.Etapas), c.Etapas)
	}
	e := c.Etapas[0]
	if e.Estado != "done" || e.Ms == nil || *e.Ms != 2400 {
		t.Errorf("nao atualizou: %+v", e)
	}
	if e.Numeros["paginas"] != 300.0 {
		t.Errorf("os numeros da etapa se perderam: %+v", e.Numeros)
	}
}

// A ordem de chegada e a ordem da tela: extract antes de load, sempre.
func TestOrdemDeChegadaEPreservada(t *testing.T) {
	var c coletorDeEtapas
	c.linha(`@brevis:{"tipo":"etapa","nome":"check","estado":"done"}`)
	c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	c.linha(`@brevis:{"tipo":"etapa","nome":"load","estado":"running"}`)
	c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"done"}`)

	querido := []string{"check", "extract", "load"}
	for i, nome := range querido {
		if c.Etapas[i].Nome != nome {
			t.Fatalf("ordem: %+v, esperada %v", c.Etapas, querido)
		}
	}
}

// Uma etapa que este motor nao conhece e ignorada, em vez de virar um bloco
// sem sentido na tela. O SDK pode ganhar etapas antes de o motor saber delas.
func TestEtapaDesconhecidaEIgnorada(t *testing.T) {
	var c coletorDeEtapas
	if !c.linha(`@brevis:{"tipo":"etapa","nome":"reticulando","estado":"running"}`) {
		t.Error("a linha e marca e devia ser consumida mesmo desconhecida")
	}
	if len(c.Etapas) != 0 {
		t.Errorf("inventou um bloco: %+v", c.Etapas)
	}
}

// O selo e OBSERVADO: se ele existe, o SDK rodou. Nada no YAML o produz, entao
// ele nao tem como mentir -- e um selo errado seria pior que selo nenhum,
// porque ele e justamente o que se olha para descartar hipoteses.
func TestSeloVemDoAnuncio(t *testing.T) {
	var c coletorDeEtapas
	if c.Versao != "" {
		t.Error("um passo que nao disse nada nao pode ter versao")
	}
	if !c.linha(`@brevis:{"tipo":"sdk","versao":"v0.44.1","pipeline":"fetcher"}`) {
		t.Fatal("o anuncio nao foi reconhecido")
	}
	if c.Versao != "v0.44.1" {
		t.Errorf("versao = %q", c.Versao)
	}
	// E o anuncio nao inventa um bloco na tela: ele so carrega o selo.
	if len(c.Etapas) != 0 {
		t.Errorf("o anuncio virou etapa: %+v", c.Etapas)
	}
}

// O teto existe porque cada transicao vira escrita em banco. Sem ele, um
// pipeline em laco derrubaria o Postgres pelo caminho do log -- e o log e o
// que nao pode parar de funcionar quando algo esta errado.
func TestTetoProtegeOBanco(t *testing.T) {
	var c coletorDeEtapas
	for i := 0; i < tetoDeEtapas*3; i++ {
		c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	}
	if c.vistos > tetoDeEtapas {
		t.Errorf("registrou %d transicoes, o teto e %d", c.vistos, tetoDeEtapas)
	}
}
