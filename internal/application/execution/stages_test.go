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
func TestAPlainLineStaysALogLine(t *testing.T) {
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
	for i, name := range querido {
		if c.Stages[i].TaskName != name {
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
func TestTheCeilingProtectsTheDatabase(t *testing.T) {
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
	for name, line := range casos {
		t.Run(name, func(t *testing.T) {
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
func TestTheBadgeInBothFormats(t *testing.T) {
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

// The trend table's numbers, flattened out of the phases.
//
// Every case here has a twin in migration 00012's backfill, which applies the
// same rule in SQL over the same JSONB. Two implementations of one rule is the
// shape that drifts, so the cases are written once and checked on both sides --
// this test for the write path, TestTheBackfillAgreesWithTheRunner (in the
// postgres package, needs a database) for the migration.
func TestTheLoadNumbersComeOutOfThePhases(t *testing.T) {
	feed := func(c *stageCollector, lines ...string) {
		t.Helper()
		for _, l := range lines {
			if !c.line(l) {
				t.Fatalf("not recognised: %s", l)
			}
		}
	}

	t.Run("the ordinary pipeline", func(t *testing.T) {
		var c stageCollector
		feed(&c,
			`@brevis:{"type":"stage","index":0,"name":"extract","state":"done","at":"x","ms":31000,"pages":12,"bytes":5000000,"http_attempts":13}`,
			`@brevis:{"type":"stage","index":1,"name":"load","state":"done","at":"x","ms":22000,"rows":48213,"records":48300,"load_bytes":900000,"ignored":7}`,
		)
		n, ok := c.LoadNumbers()
		if !ok {
			t.Fatal("a finished load reported nothing")
		}
		want := LoadNumbers{
			Rows: 48213, Records: 48300, Ignored: 7, BytesOut: 900000, LoadMs: 22000,
			BytesIn: 5000000, Pages: 12, HTTPAttempts: 13, ExtractMs: 31000,
		}
		if n != want {
			t.Errorf("got %+v\nwant %+v", n, want)
		}
	})

	// The case a fixed slot would get wrong. Two Map stages push load to index
	// three, and `etapas[1]` there is a map -- whose numbers would be filed in
	// a load's column and read as a collapse in rows loaded.
	t.Run("two map stages push load off position one", func(t *testing.T) {
		var c stageCollector
		feed(&c,
			`@brevis:{"type":"stage","index":0,"name":"extract","state":"done","at":"x","ms":40000,"bytes":9000000}`,
			`@brevis:{"type":"stage","index":1,"name":"map","state":"done","at":"x"}`,
			`@brevis:{"type":"stage","index":2,"name":"map","state":"done","at":"x"}`,
			`@brevis:{"type":"stage","index":3,"name":"load","state":"done","at":"x","ms":50000,"rows":99}`,
		)
		n, ok := c.LoadNumbers()
		if !ok || n.Rows != 99 || n.LoadMs != 50000 {
			t.Errorf("read the wrong phase: %+v (ok=%v)", n, ok)
		}
	})

	// A load that did not finish measured half of something that never
	// happened. Averaged into a trend it reads as a dataset shrinking.
	t.Run("a load that failed is not a measurement", func(t *testing.T) {
		var c stageCollector
		feed(&c,
			`@brevis:{"type":"stage","index":0,"name":"extract","state":"done","at":"x","ms":40000}`,
			`@brevis:{"type":"stage","index":1,"name":"load","state":"failed","at":"x","ms":900,"rows":40000}`,
		)
		if n, ok := c.LoadNumbers(); ok {
			t.Errorf("a failed load was recorded as a measurement: %+v", n)
		}
	})

	// Still running when the process died. The screen turns this into
	// `aborted` on read; the trend must not count it at all.
	t.Run("a load still running is not a measurement", func(t *testing.T) {
		var c stageCollector
		feed(&c, `@brevis:{"type":"stage","index":1,"name":"load","state":"running","at":"x"}`)
		if _, ok := c.LoadNumbers(); ok {
			t.Error("an unfinished load was recorded")
		}
	})

	// Most steps. A shell command is not a pipeline and has nothing to say
	// about a load trend; a row of zeros from it would drag every average it
	// touches toward a floor that never happened.
	t.Run("a step that is not a pipeline reports nothing", func(t *testing.T) {
		var c stageCollector
		if _, ok := c.LoadNumbers(); ok {
			t.Error("a step with no phases at all was recorded")
		}
	})

	// A pipeline whose source is already in memory announces no extract. Its
	// load is still worth a row -- with the extract's columns at zero, which is
	// true rather than missing.
	t.Run("a load with no extract phase", func(t *testing.T) {
		var c stageCollector
		feed(&c, `@brevis:{"type":"stage","index":0,"name":"load","state":"done","at":"x","ms":1200,"rows":5}`)
		n, ok := c.LoadNumbers()
		if !ok {
			t.Fatal("a load with no extract reported nothing")
		}
		if n.Rows != 5 || n.LoadMs != 1200 || n.BytesIn != 0 || n.ExtractMs != 0 {
			t.Errorf("got %+v", n)
		}
	})

	// The numbers arrive through encoding/json, so everything is a float64 --
	// and `detail` and `strategy` are strings sitting in the same map. A string
	// where a number was expected must read as zero rather than take the step's
	// bookkeeping down.
	t.Run("a string where a number was expected", func(t *testing.T) {
		var c stageCollector
		feed(&c, `@brevis:{"type":"stage","index":0,"name":"load","state":"done","at":"x","rows":"many","records":3}`)
		n, ok := c.LoadNumbers()
		if !ok || n.Rows != 0 || n.Records != 3 {
			t.Errorf("got %+v (ok=%v)", n, ok)
		}
	})
}
