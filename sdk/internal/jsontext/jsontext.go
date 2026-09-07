// Package jsontext writes JSON text the way Python writes it.
//
// It is a LEAF package -- only the stdlib, and not even much of that beyond
// utf8 -- on purpose: pycompat needs it and must not drag net/http along, which
// is what happened when this function lived in core. The pruning check caught
// it.
package jsontext

import (
	"unicode/utf8"
)

// AppendJSONString writes s as a JSON string, without escaping HTML.
//
// It exists in one place because the same rule is needed in two: the NDJSON
// Redshift reads, and the canonical form that reproduces Python's json.dumps.
// encoding/json escapes `<`, `>` and `&` by default and Python escapes none of
// the three -- and a difference in escaping changes the KEY, with no error.
//
// The knowledge existed in the Redshift driver and was not shared; the second
// time it was needed it cost ninety lines written again.
//
// The output is compared byte for byte against encoding/json configured without
// HTML escaping, in a test -- the claim is not "it is right", it is "it is
// identical to the stdlib".
func AppendJSONString(dst []byte, s string) []byte {
	if isPlainJSON(s) {
		dst = append(dst, '"')
		dst = append(dst, s...)
		return append(dst, '"')
	}

	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '"':
				dst = append(dst, `\"`...)
			case '\\':
				dst = append(dst, `\\`...)
			case '\n':
				dst = append(dst, `\n`...)
			case '\r':
				dst = append(dst, `\r`...)
			case '\t':
				dst = append(dst, `\t`...)
			default:
				const hex = "0123456789abcdef"
				dst = append(dst, `\u00`...)
				dst = append(dst, hex[b>>4], hex[b&0xF])
			}
			i++
			start = i
			continue
		}

		r, tamanho := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && tamanho == 1 {
			// An invalid byte. Go 1.25's encoding/json writes the escaped
			// sequence and 1.27's writes U+FFFD's bytes; both are the same code
			// point, and the test compares the VALUE in those cases.
			dst = append(dst, s[start:i]...)
			dst = append(dst, "\ufffd"...)
			i += tamanho
			start = i
			continue
		}
		i += tamanho
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// isPlainJSON says whether the string can go inside quotes with no escaping at
// all.
func isPlainJSON(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == '"' || b == '\\' || b >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
