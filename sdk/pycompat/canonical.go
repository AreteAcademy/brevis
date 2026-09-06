package pycompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/AreteAcademy/brevis/sdk/internal/jsontext"
)

// CanonicalJSON serializes the record the way Python serializes it in
//
//	json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
//
// which is how a good share of the Python fetchers derive the key when the
// source has no stable id.
//
//	Key: func(p any) (string, error) {
//	    b, err := pycompat.CanonicalJSON(p)
//	    return string(b), err
//	}
//
// # The three traps, and why this is a function
//
// Reproducing it by hand costs ~90 lines, and each of the three changes the key
// WITH NO ERROR:
//
//  1. encoding/json escapes `<`, `>` and `&`; Python escapes none of the three.
//     The rule lives in jsontext.AppendJSONString, in one place only;
//  2. without Source.PreserveNumbers, `1` and `1.0` arrive as the same float64
//     -- and here that is an ERROR, not a guess. See Text;
//  3. an arbitrary-precision integer loses precision on its way through a
//     float64, and json.Number preserves the literal.
//
// # What it is NOT
//
// It matches PYTHON, and not a standard. Somebody starting a new ETL who just
// wants a stable key wants something else -- RFC 8785 (JCS) -- and the two must
// not be the same function: confusing them would be worse than having neither.
func CanonicalJSON(v any) ([]byte, error) {
	return write(make([]byte, 0, 256), v, Text)
}

// CanonicalJSONAcceptingFloat64 is CanonicalJSON for COMPUTED records.
//
// CanonicalJSON refuses a bare float64 for the same reason Text refuses one: in
// a DECODED value there is no way to know whether the source saw an integer or
// a decimal, and `1` against `1.0` changes the key.
//
// In a value you COMPUTED -- an average, a rounding -- that ambiguity does not
// exist: it is decimal by definition, and there never was a literal. Use this
// one when the record is yours.
//
//	row["average"] = total / n
//	b, err := pycompat.CanonicalJSONAcceptingFloat64(row)
//
// The way out existed for the scalar (TextAcceptingFloat64) and was missing for
// the composite -- which is exactly where the records built by whoever
// aggregates land. Without it, the workaround was handing over a json.Number
// whose literal carried a dot: without the dot, `48` comes out as `48` where
// the reference writes `48.0`, and the consumer ends up reimplementing half of
// decimal formatting to feed decimal formatting.
func CanonicalJSONAcceptingFloat64(v any) ([]byte, error) {
	return write(make([]byte, 0, 256), v, TextAcceptingFloat64)
}

func write(dst []byte, v any, render func(any) (string, error)) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		// null, and not "None": this is JSON, and json.dumps writes JSON. Only
		// the NUMBERS follow Python's repr, which is where the two languages
		// diverge.
		return append(dst, "null"...), nil

	case bool:
		if t {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil

	case string:
		return jsontext.AppendJSONString(dst, t), nil

	case map[string]any:
		// sort_keys=True. A Go map's order is shuffled on purpose, so without
		// this the key would change on every run -- and identity with it.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		dst = append(dst, '{')
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',') // separators=(",", ":"): no spaces
			}
			dst = jsontext.AppendJSONString(dst, k)
			dst = append(dst, ':')
			var err error
			if dst, err = write(dst, t[k], render); err != nil {
				return nil, fmt.Errorf("at %q: %w", k, err)
			}
		}
		return append(dst, '}'), nil

	case []any:
		dst = append(dst, '[')
		for i, e := range t {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = write(dst, e, render); err != nil {
				return nil, fmt.Errorf("at index %d: %w", i, err)
			}
		}
		return append(dst, ']'), nil

	default:
		// A number. Text already is Python's repr for a float and the literal
		// for an int, which is exactly what json.dumps writes -- and it
		// REFUSES a bare float64, for the same reason this function cannot
		// guess.
		text, err := render(v)
		if err != nil {
			return nil, err
		}
		if _, isText := v.(string); isText {
			return jsontext.AppendJSONString(dst, text), nil
		}
		if !isNumber(text) {
			return nil, fmt.Errorf("CanonicalJSON does not know how to serialize a %T. It "+
				"covers what a JSON record produces: nil, bool, string, number, object "+
				"and list", v)
		}
		return append(dst, text...), nil
	}
}

// isNumber checks that the text is a JSON number literal.
//
// The check exists because Text renders a bool as "True" and nil as "None",
// which are Python and not JSON -- and both were already handled above. It is
// the net that stops a new type from escaping through the default and coming
// out as an invalid literal.
func isNumber(s string) bool {
	if s == "" {
		return false
	}
	n := json.Number(s)
	if _, err := n.Float64(); err != nil {
		return false
	}
	return !strings.ContainsAny(s, `"' `)
}
