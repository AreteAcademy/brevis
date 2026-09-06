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
	if e.State != "done" || e.Ms == nil || *e.Ms != 2400 {
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

// O motor tem de entender os DOIS formatos.
//
// O SDK ate a v0.47.0 falava em portugues; da v0.48.0 em diante fala ingles. Um
// motor que so entendesse o novo faria as etapas de um fetcher antigo sumirem
// da tela -- sem erro, sem log, so a caixa cinza de volta.
func TestOsDoisFormatosDoProtocolo(t *testing.T) {
	casos := map[string]string{
		"ingles (v0.48+)":         `@brevis:{"type":"stage","name":"extract","state":"done","ms":2400,"at":"agora","paginas":300}`,
		"portugues (ate a v0.47)": `@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"em":"agora","paginas":300}`,
	}
	for nome, linha := range casos {
		t.Run(nome, func(t *testing.T) {
			var c coletorDeEtapas
			if !c.linha(linha) {
				t.Fatal("a marca nao foi reconhecida")
			}
			if len(c.Etapas) != 1 {
				t.Fatalf("etapas: %+v", c.Etapas)
			}
			e := c.Etapas[0]
			if e.Nome != "extract" || e.State != "done" || e.Ms == nil || *e.Ms != 2400 {
				t.Errorf("etapa: %+v", e)
			}
			// E os numeros da etapa nao podem trazer os campos do protocolo.
			if e.Numeros["paginas"] != 300.0 {
				t.Errorf("numeros: %+v", e.Numeros)
			}
			for _, reservado := range []string{"tipo", "type", "nome", "name", "estado", "state", "em", "at"} {
				if _, tem := e.Numeros[reservado]; tem {
					t.Errorf("o campo de protocolo %q vazou para os numeros: %+v", reservado, e.Numeros)
				}
			}
		})
	}
}

// O selo, nos dois formatos.
func TestSeloNosDoisFormatos(t *testing.T) {
	for _, linha := range []string{
		`@brevis:{"type":"sdk","version":"v0.48.0","pipeline":"f"}`,
		`@brevis:{"tipo":"sdk","versao":"v0.47.0","pipeline":"f"}`,
	} {
		var c coletorDeEtapas
		if !c.linha(linha) || c.Versao == "" {
			t.Errorf("versao nao chegou de %q: %q", linha, c.Versao)
		}
	}
}

// Dois `map` no mesmo pipeline sao DUAS caixas.
//
// Chavear por nome fazia o segundo sobrescrever o primeiro: tres estagios
// declarados viravam duas caixas na tela, sem aviso.
func TestDoisEstagiosDeMesmoNomeSaoDuasCaixas(t *testing.T) {
	var c coletorDeEtapas
	for _, l := range []string{
		`@brevis:{"type":"stage","index":0,"name":"map","state":"done","in":100,"out":90}`,
		`@brevis:{"type":"stage","index":1,"name":"aggregate","state":"done","in":90,"out":9,"groups":9}`,
		`@brevis:{"type":"stage","index":2,"name":"map","state":"done","in":9,"out":9}`,
	} {
		if !c.linha(l) {
			t.Fatalf("marca nao reconhecida: %s", l)
		}
	}
	if len(c.Etapas) != 3 {
		t.Fatalf("viraram %d caixas, esperado 3: %+v", len(c.Etapas), c.Etapas)
	}
	// E na ordem do pipeline, que e o que a tela desenha.
	for i, quero := range []string{"map", "aggregate", "map"} {
		if c.Etapas[i].Nome != quero || c.Etapas[i].Indice != i {
			t.Errorf("posicao %d: %+v, esperado %q", i, c.Etapas[i], quero)
		}
	}
	// O primeiro map nao foi engolido pelo segundo.
	if c.Etapas[0].Numeros["in"] != 100.0 {
		t.Errorf("o primeiro map perdeu os numeros: %+v", c.Etapas[0].Numeros)
	}
}

// As linhas podem chegar fora de ordem; a tela nao pode.
func TestAsCaixasSaemNaOrdemDoPipeline(t *testing.T) {
	var c coletorDeEtapas
	c.linha(`@brevis:{"type":"stage","index":3,"name":"load","state":"running"}`)
	c.linha(`@brevis:{"type":"stage","index":0,"name":"check","state":"done"}`)
	c.linha(`@brevis:{"type":"stage","index":1,"name":"extract","state":"done"}`)

	for i, quero := range []string{"check", "extract", "load"} {
		if c.Etapas[i].Nome != quero {
			t.Fatalf("ordem: %+v", c.Etapas)
		}
	}
}
