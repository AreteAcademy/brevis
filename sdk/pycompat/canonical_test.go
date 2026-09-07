package pycompat

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestCanonicalJSONAgainstRealPython is the differential test: the claim is not
// "it is right", it is "it is byte for byte what json.dumps produces".
//
// Reproducing that by hand cost ~90 lines in the consumer, and the three traps
// change the key with no error.
func TestCanonicalJSONAgainstRealPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}

	cases := []struct {
		name    string
		value   any
		literal string
	}{
		{"a simple object", obj(`{"b":1,"a":2}`), `{"b":1,"a":2}`},
		{"sorted keys", obj(`{"z":1,"a":2,"m":3}`), `{"z":1,"a":2,"m":3}`},
		{"nested", obj(`{"c":{"z":"x","y":"w"},"a":1}`), `{"c":{"z":"x","y":"w"},"a":1}`},
		{"a list", obj(`{"a":[1,2.0,null,true]}`), `{"a":[1,2.0,null,true]}`},
		{"unescaped html", obj(`{"h":"<a href='x'>&amp;</a>"}`), `{"h":"<a href='x'>&amp;</a>"}`},
		{"raw unicode", obj(`{"d":"acentuação e 🎉"}`), `{"d":"acentuação e 🎉"}`},
		{"int and float kept apart", obj(`{"f":19.0,"i":19}`), `{"f":19.0,"i":19}`},
		{"a large integer", obj(`{"n":9007199254740993}`), `{"n":9007199254740993}`},
		{"negative", obj(`{"n":-20.04}`), `{"n":-20.04}`},
		{"empty", obj(`{}`), `{}`},
		{"an empty list", obj(`{"a":[]}`), `{"a":[]}`},
		{"quotes and a backslash", obj(`{"s":"com \"aspas\" e \\ barra"}`), `{"s":"com \"aspas\" e \\ barra"}`},
		{"control characters", obj(`{"s":"quebra\nlinha\ttab"}`), `{"s":"quebra\nlinha\ttab"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalJSON(c.value)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}

			script := "import json,sys\n" +
				"v = json.loads(r'''" + c.literal + "''')\n" +
				"sys.stdout.write(json.dumps(v, sort_keys=True, separators=(',',':'), ensure_ascii=False))"
			b, err := exec.Command("python3", "-c", script).Output()
			if err != nil {
				t.Fatalf("python3: %v", err)
			}

			if string(got) != string(b) {
				t.Errorf("diverged:\n  ours    %s\n  Python  %s", got, b)
			}
		})
	}
}

// obj decodes preserving the number's literal, which is what
// Source.PreserveNumbers delivers.
func obj(s string) map[string]any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		panic(err)
	}
	return m
}

// TestCanonicalJSONRefusesFloat64: trap 2, and it is an error rather than a
// guess.
func TestCanonicalJSONRefusesFloat64(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"n":19}`), &m); err != nil {
		t.Fatal(err)
	}
	_, err := CanonicalJSON(m)
	if err == nil {
		t.Fatal("a float64 got through; the literal was already lost and it guessed")
	}
	for _, want := range []string{`"n"`, "PreserveNumbers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

// TestCanonicalJSONPreservesALargeInteger: trap 3 -- 2^53+1 does not survive a
// float64.
func TestCanonicalJSONPreservesALargeInteger(t *testing.T) {
	got, err := CanonicalJSON(obj(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"n":9007199254740993}` {
		t.Errorf("= %s; the precision was lost", got)
	}
}

// TestCanonicalJSONDoesNotEscapeHTML: trap 1, and the SDK already knew about it
// -- in the Redshift driver, unshared.
func TestCanonicalJSONDoesNotEscapeHTML(t *testing.T) {
	got, err := CanonicalJSON(obj(`{"h":"<&>"}`))
	if err != nil {
		t.Fatal(err)
	}
	// The assertion is about the ABSENCE of the escape: encoding/json writes
	// \u003c, and Python writes the character. Written the other way round, it
	// failed precisely when the behaviour was right.
	if strings.Contains(string(got), `\u003c`) {
		t.Errorf("escaped HTML the way encoding/json does: %s", got)
	}
	if string(got) != `{"h":"<&>"}` {
		t.Errorf("= %s", got)
	}
}

// TestCanonicalJSONIsDeterministic: a Go map's order is shuffled on purpose, and
// without sort_keys the key would change on every run.
func TestCanonicalJSONIsDeterministic(t *testing.T) {
	m := obj(`{"z":1,"a":2,"m":3,"b":4,"y":5}`)
	var first string
	for i := 0; i < 50; i++ {
		got, err := CanonicalJSON(m)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = string(got)
			continue
		}
		if string(got) != first {
			t.Fatalf("varied between runs:\n  %s\n  %s", first, got)
		}
	}
	if first != `{"a":2,"b":4,"m":3,"y":5,"z":1}` {
		t.Errorf("= %s; the keys are not sorted", first)
	}
}

// TestCanonicalJSONRefusesWhatItDoesNotKnow: a type a JSON record does not
// produce is an error naming the path, and not a guess inside a key.
func TestCanonicalJSONRefusesWhatItDoesNotKnow(t *testing.T) {
	_, err := CanonicalJSON(map[string]any{"a": map[string]any{"b": struct{}{}}})
	if err == nil {
		t.Fatal("a struct got through")
	}
	if !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("the error does not say the path: %v", err)
	}
}

// The way out existed for the scalar and was missing for the composite -- which
// is where the records built by whoever aggregates land. An average is decimal
// by definition: there is no literal to preserve, and nothing to guess.
func TestCanonicalJSONAcceptingFloat64(t *testing.T) {
	row := map[string]any{"media": 48.0, "n": 3.5, "nome": "sul"}

	if _, err := CanonicalJSON(row); err == nil {
		t.Fatal("the strict one has to go on refusing a bare float64")
	}

	b, err := CanonicalJSONAcceptingFloat64(row)
	if err != nil {
		t.Fatalf("the permissive one refused a computed record: %v", err)
	}
	// 48.0 and not 48: it is what Python's json.dumps writes for a float.
	if got, want := string(b), `{"media":48.0,"n":3.5,"nome":"sul"}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// And it descends into structures: the problem shows up precisely in the
// composite.
func TestCanonicalJSONAcceptingFloat64Nested(t *testing.T) {
	b, err := CanonicalJSONAcceptingFloat64(map[string]any{
		"tot": []any{1.0, map[string]any{"x": 2.0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"tot":[1.0,{"x":2.0}]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
