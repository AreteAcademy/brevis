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

func TestTheTypesAreValidated(t *testing.T) {
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
// decision becomes explicit, and the workflow's author owns it.
func TestPatternWidensWhatIsAccepted(t *testing.T) {
	p := wf.Param{Name: "json", Type: wf.ParamString, Pattern: `^\{"[a-z_]+":"[a-z]+"\}$`}
	if err := p.Accepts(`{"load_full":"true"}`); err != nil {
		t.Errorf("the author's pattern was ignored: %v", err)
	}
}

// An invalid default would only surface on the first scheduled run, at 3am.
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

// A list param carries many values through a map[string]string, because that is
// what every param is from the form to the step's environment.
func TestAListParamValidatesEachItem(t *testing.T) {
	tabelas := wf.Param{Name: "tables", Type: "list|string"}
	numeros := wf.Param{Name: "days", Type: "list|integer"}

	aceita := []struct {
		p     wf.Param
		value string
		why   string
	}{
		{tabelas, "users,orders", "the ordinary case"},
		{tabelas, "users", "one item is still a list"},
		{tabelas, "", "nobody filled it in"},
		{tabelas, "users, orders", "the space after a comma is the form's, not the value's"},
		{numeros, "1,7,30", "integers"},
	}
	for _, c := range aceita {
		if err := c.p.Accepts(c.value); err != nil {
			t.Errorf("%s: Accepts(%q) = %v, want nil", c.why, c.value, err)
		}
	}

	recusa := []struct {
		p     wf.Param
		value string
		why   string
	}{
		{numeros, "1,x,30", "an item that is not an integer"},
		{tabelas, "users,,orders", "an empty item"},
		{tabelas, "users,users", "the same item twice"},
		{tabelas, "users;rm -rf /", "a character the shell interprets"},
	}
	for _, c := range recusa {
		if err := c.p.Accepts(c.value); err == nil {
			t.Errorf("%s: Accepts(%q) = nil, want an error", c.why, c.value)
		}
	}
}

// The error names WHICH item failed. "1,x,30 is not an integer" would make the
// author check three values by hand.
func TestAListErrorNamesTheItem(t *testing.T) {
	p := wf.Param{Name: "days", Type: "list|integer"}
	err := p.Accepts("1,x,30")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "item 2") || !strings.Contains(err.Error(), `"x"`) {
		t.Errorf("the error does not name the item that failed: %v", err)
	}
}

// An enum on a list restricts each ITEM, which is what makes it a multi-select.
func TestAnEnumOnAListRestrictsEachItem(t *testing.T) {
	p := wf.Param{Name: "layers", Type: "list|string", Enum: []string{"bronze", "silver", "gold"}}
	if err := p.Accepts("bronze,gold"); err != nil {
		t.Errorf("two allowed items were refused: %v", err)
	}
	if err := p.Accepts("bronze,platinum"); err == nil {
		t.Error("an item outside the enum was accepted")
	}
}

func TestItemsSplitsAndTrims(t *testing.T) {
	p := wf.Param{Name: "tables", Type: "list|string"}
	got := p.Items(" users , orders ")
	if len(got) != 2 || got[0] != "users" || got[1] != "orders" {
		t.Errorf("Items() = %#v", got)
	}
	// Empty is no items, not one empty item: a `for` over one empty element
	// runs the body once on nothing.
	if got := p.Items(""); len(got) != 0 {
		t.Errorf("Items(\"\") = %#v, want nothing", got)
	}
	// A scalar is never split, however many commas it holds -- a `--select`
	// value legitimately has them.
	escalar := wf.Param{Name: "select", Type: wf.ParamString}
	if got := escalar.Items("a,b"); got != nil {
		t.Errorf("a scalar param was split: %#v", got)
	}
}

func TestTheListDeclarationIsChecked(t *testing.T) {
	recusa := []struct {
		p   wf.Param
		why string
	}{
		{wf.Param{Name: "x", Type: "list|"}, "no element type"},
		{wf.Param{Name: "x", Type: "list|date"}, "an element type that does not exist"},
		{wf.Param{Name: "x", Type: "list|list|string"}, "a list of lists"},
		// The enum's values are compared against ITEMS, so one holding the
		// separator could never match.
		{wf.Param{Name: "x", Type: "list|string", Enum: []string{"a,b"}}, "a comma inside an enum value"},
		// A default is validated by the same rules, because a refused one would
		// only surface on the first scheduled run.
		{wf.Param{Name: "x", Type: "list|integer", Default: "1,x"}, "a default that is not valid"},
	}
	for _, c := range recusa {
		if err := c.p.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", c.why)
		}
	}
	ok := wf.Param{Name: "tables", Type: "list|string", Default: "users,orders"}
	if err := ok.Validate(); err != nil {
		t.Errorf("a valid list param was refused: %v", err)
	}
}
