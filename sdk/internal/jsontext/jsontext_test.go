package jsontext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestAppendJSONStringAgreesWithTheEncoder compares byte for byte against
// encoding/json with HTML escaping off.
//
// The claim is not "mine is right": it is "it is identical to the stdlib's".
// Writing
// JSON by hand is how you produce a file the server reads differently, and
// here is worse -- the same rule composes a KEY.
func TestAppendJSONStringAgreesWithTheEncoder(t *testing.T) {
	casos := []struct {
		s           string
		soSemantico bool
	}{
		{"", false}, {"simples", false}, {"com espaço", false},
		{`aspas "no meio"`, false},
		{`contrabarra \ e \\ dupla`, false},
		{"quebra\nde\rlinha\te tab", false},
		{"controle \x00\x01\x1f no meio", false},
		{"acentuação e emoji 🎉", false},
		{"html < > & sem escape", false},
		{string([]byte{0xff, 0xfe}), true},
		{"válido " + string([]byte{0xff}) + " misturado", true},
	}

	for _, c := range casos {
		nome := strings.Map(func(r rune) rune {
			if r < 0x20 {
				return '_'
			}
			return r
		}, c.s)
		t.Run(nome, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(c.s); err != nil {
				t.Fatal(err)
			}
			quero := strings.TrimSuffix(buf.String(), "\n")

			got := string(AppendJSONString(nil, c.s))
			if got == quero {
				return
			}
			if !c.soSemantico {
				t.Fatalf("%q:\n  meu      %s\n  Encoder  %s", c.s, got, quero)
			}

			// Invalid UTF-8: 1.25's encoding/json escapes the U+FFFD and
			// 1.27 writes the bytes. Both forms are the same code point, and
			// the equality that counts there is of the value.
			var meu, dele string
			if err := json.Unmarshal([]byte(got), &meu); err != nil {
				t.Fatalf("a minha saída nem é JSON válido: %s", got)
			}
			if err := json.Unmarshal([]byte(quero), &dele); err != nil {
				t.Fatalf("a saída do encoder não decodifica: %s", quero)
			}
			if meu != dele {
				t.Errorf("%q decodifica diferente:\n  meu     %q\n  Encoder %q", c.s, meu, dele)
			}
		})
	}
}

// TestAppendJSONStringAppendsToTheDestination: ela recebe o buffer e devolve o
// buffer, so it does not allocate one per string on a load of millions.
func TestAppendJSONStringAppendsToTheDestination(t *testing.T) {
	dst := []byte("antes:")
	got := string(AppendJSONString(dst, "x"))
	if got != `antes:"x"` {
		t.Errorf("= %q", got)
	}
}
