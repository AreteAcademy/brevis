package workflow_test

import (
	"encoding/json"
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
		wf.Param{Name: "load_full", Type: wf.ParamBool, Default: "false"},
		wf.Param{Name: "days", Type: wf.ParamInteger, Default: "7"},
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
	w := withParams(wf.Param{Name: "load_full", Type: wf.ParamBool, Default: "false"})

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
		value string
		ok    bool
	}{
		{wf.Param{Name: "b", Type: wf.ParamBool}, "true", true},
		{wf.Param{Name: "b", Type: wf.ParamBool}, "sim", false},
		{wf.Param{Name: "n", Type: wf.ParamInteger}, "42", true},
		{wf.Param{Name: "n", Type: wf.ParamInteger}, "42.5", false},
		{wf.Param{Name: "t", Type: wf.ParamString}, "2026-09-01", true},
		{wf.Param{Name: "t", Type: wf.ParamString, Enum: []string{"a", "b"}}, "a", true},
		{wf.Param{Name: "t", Type: wf.ParamString, Enum: []string{"a", "b"}}, "c", false},
		{wf.Param{Name: "t", Type: wf.ParamString, Pattern: `^\d{4}-\d{2}-\d{2}$`}, "2026-09-01", true},
		{wf.Param{Name: "t", Type: wf.ParamString, Pattern: `^\d{4}-\d{2}-\d{2}$`}, "ontem", false},
	}
	for _, c := range casos {
		err := c.param.Accepts(c.value)
		if c.ok && err != nil {
			t.Errorf("%s=%q refused: %v", c.param.Type, c.value, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s=%q accepted and should not be", c.param.Type, c.value)
		}
	}
}

// A param's value goes INTO the step's command line, and whoever fires a run is
// not necessarily whoever wrote the workflow. Without this barrier,
// `--date {{ .data }}` with `; rm -rf /` would be arbitrary execution on the
// worker.
func TestTextRefusesAShellCharacter(t *testing.T) {
	p := wf.Param{Name: "data", Type: wf.ParamString}

	for _, veneno := range []string{
		"; rm -rf /",
		"$(whoami)",
		"`id`",
		"a | cat /etc/passwd",
		"a && curl http://malicioso",
		"a > /tmp/x",
		"'; DROP TABLE runs; --",
	} {
		if err := p.Accepts(veneno); err == nil {
			t.Errorf("aceitou %q", veneno)
		}
	}

	// And it still serves what the real params need.
	for _, legitimo := range []string{
		"2026-09-01", "bronze_id_verification+", "true", "path/to/file.csv",
		"a,b,c", "chave=valor", "50", "us-central1",
	} {
		if err := p.Accepts(legitimo); err != nil {
			t.Errorf("it refused the legitimate value %q: %v", legitimo, err)
		}
	}
}

// Whoever genuinely needs a character outside the set declares `pattern` -- the
// decisao passa a ser explicita, do autor do workflow.
func TestPatternWidensWhatIsAccepted(t *testing.T) {
	p := wf.Param{Name: "json", Type: wf.ParamString, Pattern: `^\{"[a-z_]+":"[a-z]+"\}$`}
	if err := p.Accepts(`{"load_full":"true"}`); err != nil {
		t.Errorf("the author's pattern was ignored: %v", err)
	}
}

// Default invalido so apareceria no primeiro disparo agendado, de madrugada.
func TestAnInvalidPatternFailsAtPublishTime(t *testing.T) {
	w := withParams(wf.Param{Name: "days", Type: wf.ParamInteger, Default: "muitos"})
	if err := w.Validate(); err == nil {
		t.Fatal("an invalid default value passed validation")
	}
}

func TestAnInvalidDeclarationIsRefused(t *testing.T) {
	casos := []wf.Param{
		{Name: "Load_Full", Type: wf.ParamBool},           // maiuscula
		{Name: "2days", Type: wf.ParamInteger},            // starts with a digit
		{Name: "ok", Type: "float"},                       // tipo inexistente
		{Name: "ok"},                                      // no type
		{Name: "ok", Type: wf.ParamString, Pattern: "[("}, // regex quebrada
	}
	for _, p := range casos {
		if err := p.Validate(); err == nil {
			t.Errorf("declaracao invalida aceita: %+v", p)
		}
	}
}

func TestADuplicateParamIsRefused(t *testing.T) {
	w := withParams(
		wf.Param{Name: "x", Type: wf.ParamString},
		wf.Param{Name: "x", Type: wf.ParamBool},
	)
	if err := w.Validate(); err == nil {
		t.Fatal("a duplicate param got through")
	}
}

// TestTheParamKeysAreTheOnDiskFormat pins the JSON a published workflow holds.
//
// A Workflow goes into `workflows.definicao` and `runs.definicao` as JSON with
// no tags of its own, so the Go field NAME used to be the key. Every workflow
// published before the fields were renamed holds `Nome`, `Tipo` and
// `Descricao`, and json.Unmarshal ignores a key it does not recognise: drop
// these tags and a stored param comes back with no name, no type and no
// description -- no error, no log, and a trigger form rendering an empty field
// while the validation refuses a value the author declared as valid.
//
// The count is asserted too. A field added without a tag is a key this test
// cannot see, and it would be one more thing that reads back empty.
func TestTheParamKeysAreTheOnDiskFormat(t *testing.T) {
	p := wf.Param{
		Name: "load_full", Type: wf.ParamBool, Default: "false",
		Description: "reprocesses everything", Enum: []string{"true", "false"},
		Pattern: "^(true|false)$",
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"Nome": "load_full", "Tipo": "boolean", "Default": "false",
		"Descricao": "reprocesses everything", "Pattern": "^(true|false)$",
	} {
		if got[key] != want {
			t.Errorf("key %q = %v, want %v -- this is the on-disk format, and a "+
				"published workflow read without it loses the field in silence",
				key, got[key], want)
		}
	}
	if _, ok := got["Enum"]; !ok {
		t.Error("key Enum is missing")
	}
	if len(got) != 6 {
		t.Errorf("the document has %d keys, want 6: %v -- a field with no tag is a "+
			"key this test cannot see", len(got), got)
	}
}
