package sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/pycompat"
)

// These live in the sdk package because they exercise the SEAM -- KeyWith and
// IngestionIDWith -- and not the rendering itself. pycompat is only the
// implementation that goes through it.

// TestIngestionIDWithPycompatMatchesPython is the proof that matters to whoever
// is porting: the id Go composes is the one Python composed, in the three cases
// that diverge (nil, bool and an integral float).
func TestIngestionIDWithPycompatMatchesPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}

	// source_key goes in as a json.Number, which is what PreserveNumbers
	// delivers -- Text refuses a bare float64, because by then the literal is
	// already lost.
	registro := func() map[string]any {
		return map[string]any{
			"provider": "acme", "entity": nil,
			"source_key": json.Number("19.0"), "record_ts": true,
		}
	}

	saida, err := sdk.IngestionIDWith(pycompat.Text)(registro())
	if err != nil {
		t.Fatal(err)
	}
	got := saida.(map[string]any)[sdk.ColumnIngestionID].(string)

	script := `
import uuid
ns = uuid.UUID('e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7')
chave = '|'.join(str(v) for v in ['acme', None, 19.0, True])
print(uuid.uuid5(ns, chave))`
	b, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	quero := strings.TrimSpace(string(b))
	if got != quero {
		t.Errorf("IngestionIDWith(pycompat.Text) = %s, e o Python dá %s", got, quero)
	}

	// And the default does NOT match. That is the divergence that motivated all
	// of this, and keeping it is a written decision: changing the default would
	// rewrite the id of every row Go has already written.
	saidaPadrao, err := sdk.IngestionID()(registro())
	if err != nil {
		t.Fatal(err)
	}
	if padrao := saidaPadrao.(map[string]any)[sdk.ColumnIngestionID].(string); padrao == quero {
		t.Error("o padrão passou a casar com o Python; se foi intencional, a decisão " +
			"documentada em asText mudou e este teste precisa ser reescrito")
	}
}

// TestKeyWithMatchesPython: the same, for the key.
func TestKeyWithMatchesPython(t *testing.T) {
	registro := map[string]any{"a": nil, "b": json.Number("19.0"), "c": true}

	got, err := sdk.KeyWith(pycompat.Text, "a", "b", "c")(registro)
	if err != nil {
		t.Fatal(err)
	}
	if got != "None|19.0|True" {
		t.Errorf("KeyWith = %q, o Python daria \"None|19.0|True\"", got)
	}

	padrao, err := sdk.Key("a", "b", "c")(registro)
	if err != nil {
		t.Fatal(err)
	}
	if padrao != "|19.0|true" {
		t.Errorf("Key = %q -- se mudou, o padrão mudou", padrao)
	}
}

// TestKeyWithRefusesAFloat64NamingTheField: a recusa do item 11 chega ao
// consumer through the door they use, and names the field -- without the name,
// whoever reads the error does not know which of the six it is.
func TestKeyWithRefusesAFloat64NamingTheField(t *testing.T) {
	_, err := sdk.KeyWith(pycompat.Text, "a", "b")(map[string]any{
		"a": "ok", "b": float64(19),
	})
	if err == nil {
		t.Fatal("um float64 passou; o literal já se perdeu e ele adivinhou")
	}
	if !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
	if !strings.Contains(err.Error(), "PreserveNumbers") {
		t.Errorf("o erro não oferece a saída: %v", err)
	}
}

// TestKeyWithRefusesNamingTheField: without the name, whoever reads the error
// does not know which of the six fields it is.
func TestKeyWithRefusesNamingTheField(t *testing.T) {
	_, err := sdk.KeyWith(pycompat.Text, "a", "b")(map[string]any{"a": "ok", "b": 1e-5})
	if err == nil {
		t.Fatal("a faixa exponencial passou")
	}
	if !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
}

// TestTextOrEmptyIsPythonsIdiom: `str(x or "")` is the most common form in key
// composition, and zero is the case whoever writes it by hand gets wrong.
func TestTextOrEmptyIsPythonsIdiom(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}

	casos := []struct {
		valor   any
		literal string
	}{
		{nil, "None"}, {"", "''"}, {json.Number("0.0"), "0.0"}, {int64(0), "0"},
		{false, "False"}, {[]any{}, "[]"}, {map[string]any{}, "{}"},
		{"ola", "'ola'"}, {json.Number("19.0"), "19.0"}, {true, "True"},
	}

	var literais []string
	for _, c := range casos {
		literais = append(literais, c.literal)
	}
	script := "import sys\nfor v in [" + strings.Join(literais, ", ") +
		"]:\n    sys.stdout.write(str(v or '') + '\\x00')\n"
	b, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	quero := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")

	for i, c := range casos {
		got, err := pycompat.TextOrEmpty(c.valor)
		if err != nil {
			t.Errorf("TextOrEmpty(%#v): %v", c.valor, err)
			continue
		}
		if got != quero[i] {
			t.Errorf("TextOrEmpty(%#v) = %q, e str(%s or '') = %q",
				c.valor, got, c.literal, quero[i])
		}
	}
}

// TestTheDivergenceBetweenTheDefaultAndPython is the divergence's
// documentation, as
// teste.
//
// The SDK does NOT use Python's rendering by default, and this table is the
// reason in writing: changing the default would change the ingestion_id of every
// row Go has already written.
func TestTheDivergenceBetweenTheDefaultAndPython(t *testing.T) {
	casos := []struct {
		entrada any
		padrao  string
		python  string
	}{
		{nil, "", "None"},
		{true, "true", "True"},
		{false, "false", "False"},
		{json.Number("19.0"), "19.0", "19.0"},
		{json.Number("0.0"), "0.0", "0.0"},
		// These do NOT diverge, and that is what keeps the list above short.
		{"ola", "ola", "ola"},
		{json.Number("-20.04"), "-20.04", "-20.04"},
	}

	for _, c := range casos {
		// The default is exercised through the front door, with a single-field
		// key: asText is private, and a test that reached it from inside would
		// stop proving what the consumer sees.
		padrao, err := sdk.Key("v")(map[string]any{"v": c.entrada})
		if err != nil {
			t.Fatalf("Key(%#v): %v", c.entrada, err)
		}
		if padrao != c.padrao {
			t.Errorf("Key(%#v) = %q, a tabela diz %q", c.entrada, padrao, c.padrao)
		}

		python, err := sdk.KeyWith(pycompat.Text, "v")(map[string]any{"v": c.entrada})
		if err != nil {
			t.Fatalf("KeyWith(%#v): %v", c.entrada, err)
		}
		if python != c.python {
			t.Errorf("KeyWith(%#v) = %q, a tabela diz %q", c.entrada, python, c.python)
		}
	}
}

// TestTheUserAgentIsOursAndNotGos: some public providers rate-limit or block
// Go's default UA, and that shows up as an intermittent 403 -- the kind of
// failure that costs half a morning to diagnose.
func TestTheUserAgentIsOursAndNotGos(t *testing.T) {
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, sdk.Source{From: from.HTTP{URL: srv.URL}})

	if strings.Contains(visto, "Go-http-client") {
		t.Errorf("o UA é o padrão do Go: %q", visto)
	}
	if !strings.Contains(visto, "brevis") {
		t.Errorf("o UA não identifica o SDK: %q", visto)
	}
}

// TestTheCallersUserAgentWins: whoever needs to present as something else
// continua podendo.
func TestTheCallersUserAgentWins(t *testing.T) {
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, sdk.Source{From: from.HTTP{
		URL:    srv.URL,
		Header: map[string][]string{"User-Agent": {"meu-fetcher/2.0"}},
	}})

	if visto != "meu-fetcher/2.0" {
		t.Errorf("UA = %q; o do chamador devia vencer", visto)
	}
}

// TestResponsePreserveNumbersReachesObject: whoever defines Records decodes
// on their own, and forgetting UseNumber is silent -- `1` and `1.0` become the
// same float64 and the key comes out different from the one Python composed.
func TestResponsePreserveNumbersReachesObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"id":19}]}`)
	}))
	defer srv.Close()

	tipos := map[bool]string{}
	for _, preservar := range []bool{false, true} {
		drenar(t, sdk.Source{From: from.HTTP{
			URL:             srv.URL,
			PreserveNumbers: preservar,
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				linhas := doc["results"].([]any)
				tipos[preservar] = fmt.Sprintf("%T", linhas[0].(map[string]any)["id"])
				return linhas, nil
			},
		}})
	}

	if tipos[false] != "float64" {
		t.Errorf("sem PreserveNumbers, o tipo é %s; esperado float64", tipos[false])
	}
	if tipos[true] != "json.Number" {
		t.Errorf("com PreserveNumbers, o tipo é %s; o Object devia honrar a Source", tipos[true])
	}
}

func drenar(t *testing.T, fonte sdk.Source) {
	t.Helper()
	dados, err := sdk.Extract(context.Background(), fonte)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, err := range dados.Records {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}
}
