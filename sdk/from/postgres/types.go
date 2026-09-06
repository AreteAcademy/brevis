package postgres

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ToJSON converts a Postgres value into what will become JSON.
//
// This is NOT inference: it is a written, reviewable table, with one test per
// line. Every choice here has a reason, and the reason sits next to it.
//
//	SQL                 Go              JSON
//	NUMERIC/DECIMAL     string          string     a float64 loses cents on money
//	TIMESTAMPTZ         time.Time       RFC 3339
//	DATE                time.Time       YYYY-MM-DD with no invented time
//	BYTEA               []byte          base64     encoding/json already does it
//	JSON/JSONB          RawMessage      nested     do not reserialize
//	UUID                string          string
//	NULL                nil             null
//	array               []any           array
func ToJSON(v any) any { return convert(v, 0) }

// ParaJSON is the former name of ToJSON.
//
// Deprecated: use ToJSON. It is kept because it shipped in a published
// version. It will go in v1.
func ParaJSON(v any) any { return ToJSON(v) }

// ToJSONWithOID is the version that knows the column's DECLARED type.
//
// The OID matters because pgx hands DATE and TIMESTAMPTZ over as the same
// time.Time, and only the column's type tells the two apart. Without it a DATE
// came out as "2026-09-05T00:00:00Z" -- a time nobody wrote, which walks a day
// on the first timezone conversion. Found by the integration test against the
// real server, and not by the ones that build values in memory.
func ToJSONWithOID(v any, oid uint32) any { return convert(v, oid) }

// ParaJSONComOID is the former name of ToJSONWithOID.
//
// Deprecated: use ToJSONWithOID. It is kept because it shipped in a published
// version. It will go in v1.
func ParaJSONComOID(v any, oid uint32) any { return ToJSONWithOID(v, oid) }

// The OIDs that change the conversion. The numbers come from Postgres's
// catalog and do not move: pg_type.oid is part of the protocol.
const (
	oidDate = 1082
	oidTime = 1083
)

func convert(v any, oid uint32) any {
	switch t := v.(type) {
	case nil:
		return nil

	case pgtype.Numeric:
		// Money. Becoming a float64 loses cents on large values, and the damage
		// turns up months later, in a report nobody double-checks. A string
		// preserves what came; whoever wants a number converts in Transform.
		return numericAsText(t)

	case pgtype.Date:
		if !t.Valid {
			return nil
		}
		// No time: a date carrying 00:00:00 becomes a time nobody wrote, and it
		// vanishes or walks a day when somebody converts a timezone.
		return t.Time.Format("2006-01-02")

	case time.Time:
		switch oid {
		case oidDate:
			// No time: 00:00:00 is a time nobody wrote.
			return t.Format("2006-01-02")
		case oidTime:
			return t.Format("15:04:05.999999999")
		}
		return t.UTC().Format(time.RFC3339Nano)

	case pgtype.Timestamptz:
		if !t.Valid {
			return nil
		}
		return t.Time.UTC().Format(time.RFC3339Nano)

	case pgtype.Timestamp:
		if !t.Valid {
			return nil
		}
		return t.Time.UTC().Format(time.RFC3339Nano)

	case [16]byte:
		// UUID: pgx hands the raw bytes over, and raw they would become an
		// array of numbers in the JSON.
		return formatUUID(t)

	case pgtype.UUID:
		if !t.Valid {
			return nil
		}
		return formatUUID(t.Bytes)

	case json.RawMessage:
		// JSON/JSONB go in nested. Reserializing would become a string with
		// JSON inside it, and whoever consumes it would have to decode twice.
		return nestedJSON(t)

	case []byte:
		// BYTEA. encoding/json already writes base64.
		return t

	case net.IP:
		return t.String()

	case *net.IPNet:
		if t == nil {
			return nil
		}
		return t.String()

	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			// No OID: inside a composite there is no column, and the Go type
			// already carries what can be known.
			out[k] = convert(v, 0)
		}
		return out

	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = convert(v, oid)
		}
		return out

	default:
		return v
	}
}

// numericAsText uses the exact decimal representation pgtype keeps.
func numericAsText(n pgtype.Numeric) any {
	if !n.Valid {
		return nil
	}
	if n.NaN {
		return "NaN"
	}
	if n.InfinityModifier != pgtype.Finite {
		return strings.TrimSpace(n.InfinityModifier.String())
	}
	b, err := n.MarshalJSON()
	if err != nil {
		return nil
	}
	// MarshalJSON returns the number unquoted; the string is the point.
	return strings.Trim(string(b), `"`)
}

// nestedJSON decodes so that the value goes in as a structure, and not as a
// string. Invalid JSON in the database becomes a string rather than taking the
// row down -- the data exists, and refusing it here would lose the whole record
// over one column.
func nestedJSON(raw json.RawMessage) any {
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
