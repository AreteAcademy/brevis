package redshift

import (
	"bytes"
	"strconv"

	"github.com/AreteAcademy/brevis/sdk/internal/jsontext"
)

// escreverEscalar writes the types a JSON record almost always carries, without
// going through json.Encoder. It returns false when it does not know how.
//
// It exists because of a measurement, and the measurement changed sign between
// two versions of Go: handing `any` to the Encoder cost ONE allocation per value
// on Go 1.27 -- 40,000 for 10,000 rows of four columns -- and almost none on
// 1.25, because escape analysis was different. The allocation count is not a
// property of the code; it is a property of the code plus the compiler. Writing
// the scalar straight into the buffer depends on neither.
//
// The output is compared byte for byte against json.Marshal in the test, for
// every case that usually breaks whoever writes this by hand: quotes,
// backslashes, control characters, unicode and invalid UTF-8.
func escreverEscalar(buf *bytes.Buffer, v any) bool {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeText(buf, t)
	case int:
		buf.Write(strconv.AppendInt(buf.AvailableBuffer(), int64(t), 10))
	case int32:
		buf.Write(strconv.AppendInt(buf.AvailableBuffer(), int64(t), 10))
	case int64:
		buf.Write(strconv.AppendInt(buf.AvailableBuffer(), t, 10))
	case uint64:
		buf.Write(strconv.AppendUint(buf.AvailableBuffer(), t, 10))
	case float64:
		// NaN and Inf do not exist in JSON, and the encoder refuses them. Here
		// refusing means returning false: the encoder raises the error, with its
		// own message.
		if t != t || t > 1.7976931348623157e308 || t < -1.7976931348623157e308 {
			return false
		}
		buf.Write(strconv.AppendFloat(buf.AvailableBuffer(), t, 'g', -1, 64))
	default:
		return false
	}
	return true
}

// writeText delegates to core: the same rule is needed here and in pycompat's
// canonical form, and keeping two copies of it is keeping two chances to
// diverge from Python without anybody noticing.
func writeText(buf *bytes.Buffer, s string) {
	buf.Write(jsontext.AppendJSONString(buf.AvailableBuffer(), s))
}
