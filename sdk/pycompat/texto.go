// Package pycompat renders values the way Python renders them, for anyone
// porting a Python fetcher while keeping the SAME landing table and the SAME
// ids.
//
// It lives in a subpackage, and not in the core, because it is a BRIDGE FOR A
// MIGRATION and not an ETL concept. A team starting a new pipeline in Go has
// nothing to match, and the structure says so: whoever needs it imports it,
// whoever does not never learns it exists.
//
//	Key:       sdk.KeyWith(pycompat.Text, "provider", "id"),
//	Transform: []sdk.Transformer{sdk.IngestionIDWith(pycompat.Text)},
package pycompat

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Text renders a value the way Python's `str()` renders it.
//
// It exists for anyone porting a Python fetcher to Go while keeping the SAME
// landing table and the SAME ingestion_id. If Python did `str(record["id"])` to
// compose the key, Go has to produce exactly that text -- otherwise the same
// reading gets a different id, and what shows up on the other side is not an
// error: it is a duplicated row after the bronze merge.
//
// The SDK does NOT use this function by default. See sdk.IngestionIDWith and
// sdk.KeyWith for how to ask it to.
//
//	Go (asText, the default)   Python (str)
//	nil        ""              None       "None"
//	true       "true"          True       "True"
//	19.0       "19"            19.0       "19.0"
//
// # The range where it REFUSES, and why refusing is better
//
// Python's `str()` switches to exponent notation when the decimal exponent
// leaves [-4, 16): `str(1e-5)` is `"1e-05"`, `str(1e16)` is `"1e+16"`. The exact
// shape of that text -- how many digits in the exponent, the sign, the leading
// zero -- is a CPython implementation detail, and imitating it would be betting
// that the bet is right inside a KEY.
//
// So in that range it returns an error naming the value. A key that fails loudly
// is a one-line problem; a key that diverges in silence is a duplicate that
// turns up weeks later, in a report, with nobody knowing where it came from.
//
// # What it cannot recover
//
// `encoding/json` decodes every number as a float64, so `{"id": 19}` and
// `{"id": 19.0}` reach Go identical -- and in Python the first was an `int`
// (str = "19") and the second a `float` (str = "19.0").
//
// A float64 is treated as Python's float, which is the right choice for a number
// that was a float at the source and the WRONG one for a number that was an int.
// To stop depending on that, turn on Source.PreserveNumbers: the literal arrives
// intact as a json.Number, and here it decides on its own -- with a dot or an
// exponent it is a float, without one it is an int, exactly as Python's json
// decides.
func Text(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "None", nil

	case bool:
		if t {
			return "True", nil
		}
		return "False", nil

	case string:
		return t, nil

	case json.Number:
		// The literal preserves the distinction float64 loses: Python's json
		// produces an int when there is no dot and no exponent, and a float when
		// there is.
		texto := t.String()
		if !strings.ContainsAny(texto, ".eE") {
			// int do Python. O texto do literal ja e a forma canonica, exceto
			// pelo zero a esquerda e pelo mais, que JSON nao permite.
			return texto, nil
		}
		f, err := t.Float64()
		if err != nil {
			return "", fmt.Errorf("pycompat.Text: %q is not a number: %w", texto, err)
		}
		return floatPython(f)

	case int:
		return strconv.FormatInt(int64(t), 10), nil
	case int8:
		return strconv.FormatInt(int64(t), 10), nil
	case int16:
		return strconv.FormatInt(int64(t), 10), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil

	case float32:
		// A float32 does NOT come from decoded JSON -- encoding/json never
		// produces one -- so it only reaches here if the consumer put it in the
		// record, and in that case it is unambiguously a float. It is the only
		// floating value that can be rendered without guessing.
		//
		// Converted to float64 before formatting: Python has no 32-bit float,
		// and formatting from the float32 is what Python would have seen.
		return floatPython(float64(t))

	case float64:
		// inf and nan pass: no JSON literal produces either -- JSON cannot even
		// represent them -- so the int/float ambiguity does not exist here.
		// Refusing them would apply the rule where it has no reason to.
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return floatPython(t)
		}

		// REFUSES, for the same reason the default refuses.
		//
		// A float64 only reaches here once the literal is ALREADY LOST:
		// encoding/json decodes `1` and `1.0` into the same float64, and Python
		// saw an int in one case and a float in the other. Picking either one is
		// right half the time, and the wrong half is a duplicated row weeks
		// later -- which is exactly what this function exists to prevent.
		//
		// Up to v0.39.0 it silently picked "1.0", and the limitation was
		// DOCUMENTED. Documenting a divergence is not the same as preventing it,
		// and the default just below refused for the same reason -- the
		// inconsistency was mine.
		return "", fmt.Errorf("pycompat.Text got a float64 (%v), and by now the JSON "+
			"literal is already lost: `1` and `1.0` become the same float64, and Python "+
			"saw an int in one case and a float in the other. Turn on Source.PreserveNumbers "+
			"so the number arrives as a json.Number with its literal intact. If the source "+
			"really was a float and you want it rendered as one, say so: "+
			"pycompat.TextAcceptingFloat64", t)

	default:
		return "", fmt.Errorf("pycompat.Text does not know how to render a %T the way "+
			"Python's str() would. It covers nil, bool, string, number and json.Number -- "+
			"the rest Python formats with the type's own rules, and guessing inside a key "+
			"produces a silent duplicate", v)
	}
}

// floatPython is Python's str() for a float.
func floatPython(f float64) (string, error) {
	switch {
	case math.IsNaN(f):
		return "nan", nil
	case math.IsInf(f, 1):
		return "inf", nil
	case math.IsInf(f, -1):
		return "-inf", nil
	}

	if forcaExpoente(f) {
		return "", fmt.Errorf("pycompat.Text refuses %g: in this range Python's str() uses "+
			"exponent notation (\"1e-05\", \"1e+16\"), whose exact shape is a CPython "+
			"implementation detail. Imitating it would be a bet inside a KEY -- and a key "+
			"that diverges in silence becomes a duplicate weeks later. Compose that field "+
			"yourself, or take it out of the key", f)
	}

	// 'f' with precision -1 gives the shortest decimal representation that
	// round-trips, which is the same one Python's repr uses inside this range.
	texto := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(texto, ".") {
		// Python's float always shows the decimal part: str(19.0) is "19.0", and
		// that is exactly the divergence that motivated this function.
		texto += ".0"
	}
	return texto, nil
}

// forcaExpoente diz se o Python usaria notação exponencial.
//
// A regra do repr do CPython é o expoente decimal fora de [-4, 16): abaixo de
// 1e-4 ou a partir de 1e16. O zero está dentro (str(0.0) é "0.0").
func forcaExpoente(f float64) bool {
	if f == 0 {
		return false
	}
	abs := math.Abs(f)
	return abs < 1e-4 || abs >= 1e16
}

// TextOrEmpty is Python's `str(x or "")` idiom, the most common one in key
// composition -- 14 of the fetchers surveyed use it.
//
// The `or ""` is Python's truthiness, and it is not Go's: `None`, `""`, `0`,
// `0.0`, `[]` and `{}` are all falsy there, and become an empty string. Writing
// that by hand costs about 25 lines per consumer, and getting one of the six
// cases wrong is silent -- the falsy value becomes text in the key and the id
// comes out different.
//
// Note that `0` and `0.0` become "" and not "0": it is counter-intuitive, and it
// is what Python does. A port written by hand will get None right and the zero
// wrong.
func TextOrEmpty(v any) (string, error) {
	if falsoNoPython(v) {
		return "", nil
	}
	return Text(v)
}

// falsoNoPython implements the truthiness of the types a JSON record produces.
// Anything else -- some arbitrary object -- is truthy in Python by default, and
// here falls through to Text, which refuses what it cannot render.
func falsoNoPython(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case json.Number:
		f, err := t.Float64()
		return err == nil && f == 0
	case int:
		return t == 0
	case int8:
		return t == 0
	case int16:
		return t == 0
	case int32:
		return t == 0
	case int64:
		return t == 0
	case uint:
		return t == 0
	case uint8:
		return t == 0
	case uint16:
		return t == 0
	case uint32:
		return t == 0
	case uint64:
		return t == 0
	case float32:
		return t == 0
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// TextAcceptingFloat64 e Text tratando float64 como o float do Python.
//
// Use quando voce SABE que o numero era float na origem -- e nao um int que o
// encoding/json colapsou -- e nao pode ligar Source.PreserveNumbers.
//
//	Key: sdk.KeyWith(pycompat.TextAcceptingFloat64, "lat", "lon")
//
// O nome e comprido de proposito. Ele e a afirmacao "eu conferi": num campo que
// era int no Python, isto produz "19.0" onde o Python produziu "19", e o
// resultado nao e um erro -- e uma linha duplicada depois do merge.
//
// Onde der para ligar PreserveNumbers, ligue: o literal decide sozinho, e nao ha
// o que conferir.
func TextAcceptingFloat64(v any) (string, error) {
	if f, ehFloat := v.(float64); ehFloat {
		return floatPython(f)
	}
	return Text(v)
}
