package execution

import "testing"

// The marked line is the SDK talking to the engine, not the program's output: it
// becomes a stage and DISAPPEARS from the log. Whoever watches the screen wants
// the stages, not the JSON that carried them.
func TestAMarkedLineIsConsumed(t *testing.T) {
	var c stageCollector
	if !c.line(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running","em":"agora"}`) {
		t.Fatal("the marker was not recognized")
	}
	if len(c.Stages) != 1 || c.Stages[0].TaskName != "extract" {
		t.Fatalf("etapas: %+v", c.Stages)
	}
}

// And what is NOT a marker stays a log line. Swallowing a similar-looking line
// would erase from the screen the output of a program that merely happened to
// write something alike.
func TestLinhaComumContinuaSendoLog(t *testing.T) {
	var c stageCollector
	for _, line := range []string{
		"rodando o extract",
		"@brevis",
		"@brevis:this is not json",
		`prefixo @brevis:{"tipo":"etapa","nome":"load","estado":"done"}`,
	} {
		if c.line(line) {
			t.Errorf("it swallowed a line that was a log line: %q", line)
		}
	}
	if len(c.Stages) != 0 {
		t.Errorf("it recorded a stage from a line that was not a marker: %+v", c.Stages)
	}
}

// A stage is ONE entry that changes state, not two lines of history: the screen
// shows four boxes, not a diary.
func TestAStageIsOneEntryThatChanges(t *testing.T) {
	var c stageCollector
	c.line(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	c.line(`@brevis:{"tipo":"etapa","nome":"extract","estado":"done","ms":2400,"paginas":300}`)

	if len(c.Stages) != 1 {
		t.Fatalf("became %d entries, expected 1: %+v", len(c.Stages), c.Stages)
	}
	e := c.Stages[0]
	if e.State != "done" || e.Ms == nil || *e.Ms != 2400 {
		t.Errorf("it did not update: %+v", e)
	}
	if e.Numbers["paginas"] != 300.0 {
		t.Errorf("the stage's numbers were lost: %+v", e.Numbers)
	}
}

// A ordem de chegada e a ordem da tela: extract antes de load, sempre.
func TestTheArrivalOrderIsPreserved(t *testing.T) {
	var c stageCollector
	c.line(`@brevis:{"tipo":"etapa","nome":"check","estado":"done"}`)
	c.line(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	c.line(`@brevis:{"tipo":"etapa","nome":"load","estado":"running"}`)
	c.line(`@brevis:{"tipo":"etapa","nome":"extract","estado":"done"}`)

	querido := []string{"check", "extract", "load"}
	for i, nome := range querido {
		if c.Stages[i].TaskName != nome {
			t.Fatalf("ordem: %+v, esperada %v", c.Stages, querido)
		}
	}
}

// A stage this engine does not know is ignored, rather than becoming a
// meaningless box on the screen. The SDK may gain stages before the engine knows
// about them.
func TestAnUnknownStageIsIgnored(t *testing.T) {
	var c stageCollector
	if !c.line(`@brevis:{"tipo":"etapa","nome":"reticulando","estado":"running"}`) {
		t.Error("the line is a marker and should be consumed even when unknown")
	}
	if len(c.Stages) != 0 {
		t.Errorf("inventou um bloco: %+v", c.Stages)
	}
}

// The badge is OBSERVED: if it exists, the SDK ran. Nothing in the YAML produces
// it, so it has no way to lie -- and a wrong badge would be worse than no badge,
// because it is precisely what you look at to rule hypotheses out.
func TestTheBadgeComesFromTheAnnouncement(t *testing.T) {
	var c stageCollector
	if c.Version != "" {
		t.Error("a step that said nothing cannot have a version")
	}
	if !c.line(`@brevis:{"tipo":"sdk","versao":"v0.44.1","pipeline":"fetcher"}`) {
		t.Fatal("the announcement was not recognized")
	}
	if c.Version != "v0.44.1" {
		t.Errorf("versao = %q", c.Version)
	}
	// And the announcement invents no box on the screen: it only carries the
	// badge.
	if len(c.Stages) != 0 {
		t.Errorf("the announcement became a stage: %+v", c.Stages)
	}
}

// The ceiling exists because every transition becomes a database write. Without
// it, a pipeline in a loop would take Postgres down through the log's path --
// and the log is the thing that must not stop working when something is
// wrong.
func TestTetoProtegeOBanco(t *testing.T) {
	var c stageCollector
	for i := 0; i < stageCeiling*3; i++ {
		c.line(`@brevis:{"tipo":"etapa","nome":"extract","estado":"running"}`)
	}
	if c.seen > stageCeiling {
		t.Errorf("registrou %d transicoes, o teto e %d", c.seen, stageCeiling)
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
	for nome, line := range casos {
		t.Run(nome, func(t *testing.T) {
			var c stageCollector
			if !c.line(line) {
				t.Fatal("the marker was not recognized")
			}
			if len(c.Stages) != 1 {
				t.Fatalf("etapas: %+v", c.Stages)
			}
			e := c.Stages[0]
			if e.TaskName != "extract" || e.State != "done" || e.Ms == nil || *e.Ms != 2400 {
				t.Errorf("stage: %+v", e)
			}
			// And the stage's numbers must not carry the protocol's fields.
			if e.Numbers["paginas"] != 300.0 {
				t.Errorf("numeros: %+v", e.Numbers)
			}
			for _, reservado := range []string{"tipo", "type", "nome", "name", "estado", "state", "em", "at"} {
				if _, tem := e.Numbers[reservado]; tem {
					t.Errorf("the protocol field %q leaked into the numbers: %+v", reservado, e.Numbers)
				}
			}
		})
	}
}

// The badge, in both formats.
func TestSeloNosDoisFormatos(t *testing.T) {
	for _, line := range []string{
		`@brevis:{"type":"sdk","version":"v0.48.0","pipeline":"f"}`,
		`@brevis:{"tipo":"sdk","versao":"v0.47.0","pipeline":"f"}`,
	} {
		var c stageCollector
		if !c.line(line) || c.Version == "" {
			t.Errorf("the version did not arrive from %q: %q", line, c.Version)
		}
	}
}

// Two `map`s in the same pipeline are TWO boxes.
//
// Keying by name made the second overwrite the first: three declared stages
// became two boxes on the screen, with no warning.
func TestTwoStagesWithTheSameNameAreTwoBoxes(t *testing.T) {
	var c stageCollector
	for _, l := range []string{
		`@brevis:{"type":"stage","index":0,"name":"map","state":"done","in":100,"out":90}`,
		`@brevis:{"type":"stage","index":1,"name":"aggregate","state":"done","in":90,"out":9,"groups":9}`,
		`@brevis:{"type":"stage","index":2,"name":"map","state":"done","in":9,"out":9}`,
	} {
		if !c.line(l) {
			t.Fatalf("marker not recognized: %s", l)
		}
	}
	if len(c.Stages) != 3 {
		t.Fatalf("became %d boxes, expected 3: %+v", len(c.Stages), c.Stages)
	}
	// And in the pipeline's order, which is what the screen draws.
	for i, quero := range []string{"map", "aggregate", "map"} {
		if c.Stages[i].TaskName != quero || c.Stages[i].Index != i {
			t.Errorf("position %d: %+v, expected %q", i, c.Stages[i], quero)
		}
	}
	// The first map was not swallowed by the second.
	if c.Stages[0].Numbers["in"] != 100.0 {
		t.Errorf("o primeiro map perdeu os numeros: %+v", c.Stages[0].Numbers)
	}
}

// The lines may arrive out of order; the screen may not.
func TestTheBoxesComeOutInThePipelinesOrder(t *testing.T) {
	var c stageCollector
	c.line(`@brevis:{"type":"stage","index":3,"name":"load","state":"running"}`)
	c.line(`@brevis:{"type":"stage","index":0,"name":"check","state":"done"}`)
	c.line(`@brevis:{"type":"stage","index":1,"name":"extract","state":"done"}`)

	for i, quero := range []string{"check", "extract", "load"} {
		if c.Stages[i].TaskName != quero {
			t.Fatalf("ordem: %+v", c.Stages)
		}
	}
}
