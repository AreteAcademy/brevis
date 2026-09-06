package postgres

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// toColumn converts the record's value into the Go type binary COPY requires.
//
// It exists because the two sides speak different languages, and that only
// showed up against a real server: the SDK's record is JSON -- a timestamp is
// an RFC 3339 STRING, because that is how it came out of the transformer or the
// API. pgx's binary COPY wants a time.Time, and refuses with "cannot find
// encode plan", which tells nobody that the problem is the format.
//
// As on the read side, this is NOT inference: the conversion comes from the
// column's DECLARED type, read out of information_schema. A text column gets
// the text as it came; a timestamptz gets the text parsed.
//
//	column              accepts from the record
//	timestamptz/…       RFC 3339 string, time.Time, or a number (epoch seconds)
//	date                YYYY-MM-DD string or RFC 3339
//	numeric             string, or a number
//	json/jsonb          anything -- it goes serialized
//	everything else     passes through, and the server is what refuses
func toColumn(v any, typ string) (any, error) {
	if v == nil {
		return nil, nil
	}

	switch typ {
	case "timestamp with time zone", "timestamp without time zone":
		return toInstant(v, typ)

	case "date":
		t, err := toInstant(v, typ)
		if err != nil {
			return nil, err
		}
		if tt, ok := t.(time.Time); ok {
			return tt.Truncate(24 * time.Hour), nil
		}
		return t, nil

	case "numeric":
		// A string preserves the precision the read side preserved on
		// purpose -- converting to a float here would undo that whole choice.
		//
		// But the string does not go raw: pgx tries an encode plan for
		// string->numeric, IT FAILS, and pgx builds an error just to fall
		// through to the next plan. Once per row. In the profile of a
		// 10-thousand-row load that was ~30% of the allocations, all in
		// newEncodeError, fmt.Errorf and fmt.Sprintf -- work spent producing an
		// error nobody reads.
		return toNumeric(v)

	case "json", "jsonb":
		// A map or a slice goes serialized; a string already is the document.
		switch t := v.(type) {
		case string:
			return t, nil
		case []byte:
			return string(t), nil
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("encoding as %s: %w", typ, err)
			}
			return string(b), nil
		}

	default:
		return v, nil
	}
}

// toNumeric hands over the type pgx encodes directly.
//
// A value that is not decimal text passes through as it came: the server is
// what refuses, with its own message, which beats one of ours guessing.
func toNumeric(v any) (any, error) {
	text, isText := v.(string)
	if !isText {
		return v, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(text); err != nil {
		// pgtype's error is NOT wrapped: it echoes the whole received value,
		// and a 4 KB field inside an error message reaches the log carrying
		// data nobody wants there. The size test is what caught it.
		return nil, fmt.Errorf("%q is not a number this column accepts", elide(text))
	}
	return n, nil
}

// toInstant accepts the three shapes an instant arrives in inside a record.
func toInstant(v any, typ string) (any, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil

	case string:
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05",
			"2006-01-02",
		} {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts, nil
			}
		}
		return nil, fmt.Errorf("%q is not a timestamp this column (%s) accepts: use RFC 3339, "+
			"as in 2026-09-05T12:30:00Z", elide(t), typ)

	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return nil, fmt.Errorf("%v is not a timestamp: %w", t, err)
		}
		return time.Unix(n, 0).UTC(), nil

	case float64:
		// Epoch in seconds, which is how JSON usually carries it.
		return time.Unix(int64(t), 0).UTC(), nil

	case int64:
		return time.Unix(t, 0).UTC(), nil

	case int:
		return time.Unix(int64(t), 0).UTC(), nil

	default:
		return nil, fmt.Errorf("a %T cannot go into a %s column", v, typ)
	}
}

// elide shortens a value for the error message. A 4 KB field inside an error
// message is noise, and it can carry data nobody wants in a log.
func elide(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:37] + "…"
}
