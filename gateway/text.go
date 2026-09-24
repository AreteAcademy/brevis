package gateway

import (
	"fmt"
	"strconv"
)

// text renders a JSON value as the text an attribute, an ordering key or a
// source key needs.
//
// It is NOT the SDK's asText, and the difference is deliberate: that one is
// frozen because IngestionID computes a UUID over its output, so changing it
// would rewrite every id ever written. This one is free to get stricter.
//
// The case it exists for: JSON has one number type, so an id arrives as a
// float64 and fmt.Sprint renders 26130000 as 2.613e+07. An attribute carrying
// that filters nothing, and a source_key carrying it produces an ingestion_id
// for a record that does not exist.
func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
