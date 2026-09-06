package mysql

import (
	"encoding/json"
	"strconv"
	"time"
)

// ToJSON converts a MySQL value into what will become JSON.
//
// `database/sql` returns []byte for nearly everything when read into an `any`,
// so the conversion comes out of the column's DECLARED type -- which is what
// Rows.ColumnTypes() gives. Without it every DECIMAL would become a string of
// bytes and so would every INT, and nobody would understand why the JSON is
// full of base64.
//
//	MySQL               JSON        why
//	DECIMAL/NUMERIC     string      a float64 loses cents on money
//	DATE                YYYY-MM-DD  with no invented time
//	DATETIME/TIMESTAMP  RFC 3339
//	JSON                nested      do not reserialize
//	BLOB/BINARY         base64      encoding/json already does it
//	TINYINT(1)          number      the driver cannot tell BOOL apart without the DSN
//	NULL                null
func ToJSON(v any, declared string) any {
	if v == nil {
		return nil
	}

	// A time.Time arrives when the DSN carries parseTime=true, which the driver
	// makes sure of.
	if t, ok := v.(time.Time); ok {
		if declared == "DATE" {
			// No time: 00:00:00 is a time nobody wrote.
			return t.Format("2006-01-02")
		}
		return t.UTC().Format(time.RFC3339Nano)
	}

	b, isBytes := v.([]byte)
	if !isBytes {
		return v
	}

	switch declared {
	case "DECIMAL", "NUMERIC":
		// A string preserves the precision. Money in a float64 loses cents on
		// large values, and the damage turns up months later.
		return string(b)

	case "JSON":
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			// The data exists; refusing here would lose the whole record over
			// one column.
			return string(b)
		}
		return doc

	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		// encoding/json already writes base64.
		return b

	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR":
		if n, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			return n
		}
		return string(b)

	case "UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED INT", "UNSIGNED BIGINT":
		if n, err := strconv.ParseUint(string(b), 10, 64); err == nil {
			return n
		}
		return string(b)

	case "FLOAT", "DOUBLE":
		if f, err := strconv.ParseFloat(string(b), 64); err == nil {
			return f
		}
		return string(b)

	case "DATE":
		return string(b)

	case "DATETIME", "TIMESTAMP":
		// Without parseTime the driver hands over text; normalize to RFC 3339.
		if t, err := time.Parse("2006-01-02 15:04:05.999999", string(b)); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
		return string(b)

	default:
		// CHAR, VARCHAR, TEXT, ENUM, SET and the rest are text.
		return string(b)
	}
}

// ParaJSON is the former name of ToJSON.
//
// Deprecated: use ToJSON. It is kept because it shipped in a published
// version. It will go in v1.
func ParaJSON(v any, declared string) any { return ToJSON(v, declared) }
