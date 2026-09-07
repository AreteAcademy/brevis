package execution

import "testing"

// The marked line is the SDK talking to the engine, not the program's output: it
// becomes a stage and DISAPPEARS from the log. Whoever watches the screen wants
// the stages, not the JSON that carried them.
func TestAMarkedLineIsConsumed(t *testing.T) {
	var c coletorDeEtapas
	if !c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running","em":"agora"}`) {
		t.Fatal("the marker was not recognized")
	}
	if len(c.Etapas) != 1 || c.Etapas[0].Nome != "extract" {
		t.Fatalf("etapas: %+v", c.Etapas)
	}
}

// And what is NOT a marker stays a log line. Swallowing a similar-looking line
// would erase from the screen the output of a program that merely happened to
// write something alike.
func TestLinhaComumContinuaSendoLog(t *testing.T) {
	var c coletorDeEtapas
	for _, linha := range []string{
		"rodando o extract",
		"@brevis",
		"@brevis:this is not json",
		`prefixo @brevis:{"tipo":"etapa","nome":"load","estado":"done"}`,
	} {
		if c.linha(linha) {
			t.Errorf("it swallowed a line that was a log line: %q", linha)
		}
	}
	if len(c.Etapas) != 0 {
		t.Errorf("it recorded a stage from a line that was not a marker: %+v", c.Etapas)
	}
}

// A stage is ONE entry that changes state, not two lines of history: the screen
// shows four boxes, not a diary.
func TestAStageIsOneEntryThatChanges(t *testing.T) {
	var c coletorDeEtapas
	c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	c.linha(`@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"paginas":300}`)

	if len(c.Etapas) != 1 {
		t.Fatalf("became %d entries, expected 1: %+v", len(c.Etapas), c.Etapas)
	}
	e := c.Etapas[0]
	if e.State != "done" || e.Ms == nil || *e.Ms != 2400 {
		t.Errorf("it did not update: %+v", e)
	}
	if e.Numeros["paginas"] != 300.0 {
		t.Errorf("the stage's numbers were lost: %+v", e.Numeros)
	}
}

// A ordem de chegada e a ordem da tela: extract antes de load, sempre.
func TestTheArrivalOrderIsPreserved(t *testing.T) {
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

// A stage this engine does not know is ignored, rather than becoming a
// meaningless box on the screen. The SDK may gain stages before the engine knows
// about them.
func TestAnUnknownStageIsIgnored(t *testing.T) {
	var c coletorDeEtapas
	if !c.linha(`@brevis:{"tipo":"etapa","nome":"reticulando","estado":"running"}`) {
		t.Error("the line is a marker and should be consumed even when unknown")
	}
	if len(c.Etapas) != 0 {
		t.Errorf("inventou um bloco: %+v", c.Etapas)
	}
}

// The badge is OBSERVED: if it exists, the SDK ran. Nothing in the YAML produces
// it, so it has no way to lie -- and a wrong badge would be worse than no badge,
// because it is precisely what you look at to rule hypotheses out.
func TestTheBadgeComesFromTheAnnouncement(t *testing.T) {
	var c coletorDeEtapas
	if c.Versao != "" {
		t.Error("a step that said nothing cannot have a version")
	}
	if !c.linha(`@brevis:{"tipo":"sdk","versao":"v0.44.1","pipeline":"fetcher"}`) {
		t.Fatal("the announcement was not recognized")
	}
	if c.Versao != "v0.44.1" {
		t.Errorf("versao = %q", c.Versao)
	}
	// And the announcement invents no box on the screen: it only carries the
	// badge.
	if len(c.Etapas) != 0 {
		t.Errorf("the announcement became a stage: %+v", c.Etapas)
	}
}

// The ceiling exists because every transition becomes a database write. Without
// it, a pipeline in a loop would take Postgres down through the log's path --
// and the log is the thing that must not stop working when something is
// wrong.
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
// an engine that only understood the new one would make an old fetcher's stages
// vanish from the screen -- with no error, no log, just the grey box back.
func TestBothFormatsOfTheProtocol(t *testing.T) {
	casos := map[string]string{
		"ingles (v0.48+)":         `@brevis:{"type":"stage","name":"extract","state":"done","ms":2400,"at":"agora","paginas":300}`,
		"portugues (ate a v0.47)": `@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"em":"agora","paginas":300}`,
	}
	for nome, linha := range casos {
		t.Run(nome, func(t *testing.T) {
			var c coletorDeEtapas
			if !c.linha(linha) {
				t.Fatal("the marker was not recognized")
			}
			if len(c.Etapas) != 1 {
				t.Fatalf("etapas: %+v", c.Etapas)
			}
			e := c.Etapas[0]
			if e.Nome != "extract" || e.State != "done" || e.Ms == nil || *e.Ms != 2400 {
				t.Errorf("stage: %+v", e)
			}
			// And the stage's numbers must not carry the protocol's fields.
			if e.Numeros["paginas"] != 300.0 {
				t.Errorf("numeros: %+v", e.Numeros)
			}
			for _, reservado := range []string{"tipo", "type", "nome", "name", "estado", "state", "em", "at"} {
				if _, tem := e.Numeros[reservado]; tem {
					t.Errorf("the protocol field %q leaked into the numbers: %+v", reservado, e.Numeros)
				}
			}
		})
	}
}

// The badge, in both formats.
func TestSeloNosDoisFormatos(t *testing.T) {
	for _, linha := range []string{
		`@brevis:{"type":"sdk","version":"v0.48.0","pipeline":"f"}`,
		`@brevis:{"tipo":"sdk","versao":"v0.47.0","pipeline":"f"}`,
	} {
		var c coletorDeEtapas
		if !c.linha(linha) || c.Versao == "" {
			t.Errorf("the version did not arrive from %q: %q", linha, c.Versao)
		}
	}
}

// Two `map`s in the same pipeline are TWO boxes.
//
// Keying by name made the second overwrite the first: three declared stages
// became two boxes on the screen, with no warning.
func TestTwoStagesWithTheSameNameAreTwoBoxes(t *testing.T) {
	var c coletorDeEtapas
	for _, l := range []string{
		`@brevis:{"type":"stage","index":0,"name":"map","state":"done","in":100,"out":90}`,
		`@brevis:{"type":"stage","index":1,"name":"aggregate","state":"done","in":90,"out":9,"groups":9}`,
		`@brevis:{"type":"stage","index":2,"name":"map","state":"done","in":9,"out":9}`,
	} {
		if !c.linha(l) {
			t.Fatalf("marker not recognized: %s", l)
		}
	}
	if len(c.Etapas) != 3 {
		t.Fatalf("became %d boxes, expected 3: %+v", len(c.Etapas), c.Etapas)
	}
	// And in the pipeline's order, which is what the screen draws.
	for i, quero := range []string{"map", "aggregate", "map"} {
		if c.Etapas[i].Nome != quero || c.Etapas[i].Indice != i {
			t.Errorf("position %d: %+v, expected %q", i, c.Etapas[i], quero)
		}
	}
	// The first map was not swallowed by the second.
	if c.Etapas[0].Numeros["in"] != 100.0 {
		t.Errorf("o primeiro map perdeu os numeros: %+v", c.Etapas[0].Numeros)
	}
}

// The lines may arrive out of order; the screen may not.
func TestTheBoxesComeOutInThePipelinesOrder(t *testing.T) {
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
