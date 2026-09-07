package pycompat

import (
	"encoding/json"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The table is generated from Python and pinned here, and the next test checks
// it against a real python3 when one exists.
//
// Two nets because they catch different things: the table runs on any machine
// and documents the truth in writing; the differential one catches the day
// CPython changes a formatting rule under our feet.
var howPythonRenders = []struct {
	input  any
	python string
	// literal is what Python would pass to str(), for the cases where the Go
	// value cannot express it (an int against a float).
	literal string
}{
	{nil, "None", "None"},
	{true, "True", "True"},
	{false, "False", "False"},
	{"", "", "''"},
	{"ola", "ola", "'ola'"},
	// The floats go in as json.Number, which is the real path: a bare float64
	// is refused, because by then the literal is already lost. See
	// TestTextRefusesFloat64.
	{json.Number("0.0"), "0.0", "0.0"},
	{json.Number("19.0"), "19.0", "19.0"},
	{json.Number("-20.04"), "-20.04", "-20.04"},
	{json.Number("0.1"), "0.1", "0.1"},
	{json.Number("0.3333333333333333"), "0.3333333333333333", "1/3"},
	{json.Number("1000000000000000.0"), "1000000000000000.0", "1e15"},
	{json.Number("9999000000000000.0"), "9999000000000000.0", "9.999e15"},
	{json.Number("0.0001"), "0.0001", "0.0001"},
	{int64(0), "0", "0"},
	{int64(19), "19", "19"},
	{int64(-7), "-7", "-7"},
	{json.Number("19"), "19", "19"},
	{json.Number("19.0"), "19.0", "19.0"},
	{json.Number("-20.04"), "-20.04", "-20.04"},
	{json.Number("0"), "0", "0"},
}

// TestTextAgainstTheTable is the net that runs anywhere.
func TestTextAgainstTheTable(t *testing.T) {
	for _, c := range howPythonRenders {
		got, err := Text(c.input)
		if err != nil {
			t.Errorf("Text(%#v): %v", c.input, err)
			continue
		}
		if got != c.python {
			t.Errorf("Text(%#v) = %q, Python's str() gives %q", c.input, got, c.python)
		}
	}
}

// TestTextAgainstRealPython is the differential test that was asked for.
//
// It runs a python3's str() and compares. It skips when there is no python3 --
// and skipping is honest here: the table above covers the same cases in writing,
// and a test that invented a result without Python would be differential of
// nothing.
func TestTextAgainstRealPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3; the pinned table covers the same cases")
	}

	var literals []string
	for _, c := range howPythonRenders {
		literals = append(literals, c.literal)
	}
	script := "import sys\nfor v in [" + strings.Join(literals, ", ") + "]:\n    sys.stdout.write(str(v) + '\\x00')\n"

	out, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("running python3: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(lines) != len(howPythonRenders) {
		t.Fatalf("python returned %d values for %d cases", len(lines), len(howPythonRenders))
	}

	for i, c := range howPythonRenders {
		if c.python != lines[i] {
			t.Errorf("the pinned table says str(%s) is %q, and this python3 gives %q",
				c.literal, c.python, lines[i])
		}
		got, err := Text(c.input)
		if err != nil {
			t.Errorf("Text(%#v): %v", c.input, err)
			continue
		}
		if got != lines[i] {
			t.Errorf("Text(%#v) = %q, and str(%s) = %q", c.input, got, c.literal, lines[i])
		}
	}
}

// TestTextRefusesTheExponentRange: refusing beats diverging inside a key. The
// exponent's exact shape is a CPython detail, and betting on it produces a
// duplicate in silence.
func TestTextRefusesTheExponentRange(t *testing.T) {
	// Through the door that accepts floats: the exponent policy is about
	// floats, and Text refuses a float64 before reaching it.
	refused := []float64{1e-5, 9.99e-5, -1e-5, 1e16, -1e16, 1e300, math.SmallestNonzeroFloat64}
	for _, f := range refused {
		if got, err := TextAcceptingFloat64(f); err == nil {
			t.Errorf("TextAcceptingFloat64(%g) returned %q instead of refusing", f, got)
		}
	}

	// And the inner edges pass: the range is [1e-4, 1e16).
	accepted := []float64{0, 1e-4, -1e-4, 9.999999999999998e15, 0.1}
	for _, f := range accepted {
		if _, err := TextAcceptingFloat64(f); err != nil {
			t.Errorf("TextAcceptingFloat64(%g) refused inside the range: %v", f, err)
		}
	}
}

// TestTextRefusesFloat64 is item 11 of the second round, and fixes an
// inconsistency that was mine.
//
// Text's `default` refused saying that "guessing inside a key produces a silent
// duplicate", and the `case float64` right above it guessed in silence. The
// limitation was DOCUMENTED -- and documenting a divergence is not the same as
// preventing it.
//
// A float64 only reaches here once the literal is already lost: encoding/json
// decodes `1` and `1.0` into the same float64, and Python saw an int in one case
// and a float in the other. Choosing one of the two is right half the time, and
// the wrong half is a duplicated row.
func TestTextRefusesFloat64(t *testing.T) {
	// inf and nan are left out: no JSON literal produces either, so the
	// int/float ambiguity does not exist there and the rule would have no
	// reason.
	for _, f := range []float64{0, 1, 19.0, -20.04} {
		got, err := Text(f)
		if err == nil {
			t.Errorf("Text(%v) returned %q; the literal was already lost and it guessed", f, got)
			continue
		}
		for _, want := range []string{"PreserveNumbers", "TextAcceptingFloat64"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not offer the way out %q: %v", want, err)
			}
		}
	}
}

// TestTextAcceptsFloat32: a float32 never comes out of decoded JSON, so it is a
// float with no ambiguity -- it is the only floating value that can be rendered
// without guessing.
func TestTextAcceptsFloat32(t *testing.T) {
	got, err := Text(float32(19))
	if err != nil {
		t.Fatalf("float32 refused: %v", err)
	}
	if got != "19.0" {
		t.Errorf("= %q, want \"19.0\"", got)
	}
}

// TestTextAcceptingFloat64MatchesPython: the escape hatch produces the same text
// as a Python float's str().
func TestTextAcceptingFloat64MatchesPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	cases := map[float64]string{0: "0.0", 19: "19.0", -20.04: "-20.04", 0.1: "0.1"}

	var literals []string
	var values []float64
	for f := range cases {
		values = append(values, f)
	}
	sort.Float64s(values)
	for _, f := range values {
		// An explicit float(...): Python's str(0) is "0" and str(0.0) is "0.0",
		// and a literal with no dot would become an int -- which is another
		// test.
		literals = append(literals, "float("+strconv.FormatFloat(f, 'f', -1, 64)+")")
	}
	script := "import sys\nfor v in [" + strings.Join(literals, ", ") +
		"]:\n    sys.stdout.write(str(v) + '\\x00')\n"
	b, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	want := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")

	for i, f := range values {
		got, err := TextAcceptingFloat64(f)
		if err != nil {
			t.Errorf("TextAcceptingFloat64(%v): %v", f, err)
			continue
		}
		if got != want[i] {
			t.Errorf("TextAcceptingFloat64(%v) = %q, e str(%v) = %q", f, got, f, want[i])
		}
	}
}

// TestTextRefusesWhatItDoesNotKnow: a map or a list has a str() with the type's
// own rules, and guessing inside a key is the defect this function exists to
// prevent.
func TestTextRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, v := range []any{map[string]any{"a": 1}, []any{1, 2}, struct{}{}} {
		if got, err := Text(v); err == nil {
			t.Errorf("Text(%#v) returned %q instead of refusing", v, got)
		}
	}
}

// TestTextSpecials: inf and nan have a str() and not an exponent.
func TestTextSpecials(t *testing.T) {
	cases := map[float64]string{
		math.Inf(1):  "inf",
		math.Inf(-1): "-inf",
	}
	for f, want := range cases {
		got, err := Text(f)
		if err != nil || got != want {
			t.Errorf("Text(%v) = (%q, %v), want %q", f, got, err, want)
		}
	}
	if got, _ := Text(math.NaN()); got != "nan" {
		t.Errorf("NaN = %q", got)
	}
}

// TestPreserveNumbersRecoversTheDistinctionTheFloatLoses is what makes pycompat
// genuinely usable.
//
// Without it, {"id": 19} and {"id": 19.0} arrive identical as float64(19), and
// Python saw an int in one case and a float in the other. Since v0.40.0 Text
// REFUSES that float64 instead of choosing one of the two -- because choosing is
// right half the time, and the wrong half is a duplicate.
func TestPreserveNumbersRecoversTheDistinctionTheFloatLoses(t *testing.T) {
	var withoutPreserving map[string]any
	if err := json.Unmarshal([]byte(`{"inteiro":19,"decimal":19.0}`), &withoutPreserving); err != nil {
		t.Fatal(err)
	}
	if _, err := Text(withoutPreserving["inteiro"]); err == nil {
		t.Error("without preserving, Text guessed instead of refusing")
	}

	// Preserving: the literal decides, exactly as Python's json decides.
	dec := json.NewDecoder(strings.NewReader(`{"inteiro":19,"decimal":19.0}`))
	dec.UseNumber()
	var withPreserving map[string]any
	if err := dec.Decode(&withPreserving); err != nil {
		t.Fatal(err)
	}
	integer, err := Text(withPreserving["inteiro"])
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := Text(withPreserving["decimal"])
	if err != nil {
		t.Fatal(err)
	}
	if integer != "19" || decimal != "19.0" {
		t.Errorf("with PreserveNumbers = (%q, %q), want (\"19\", \"19.0\")", integer, decimal)
	}
}
