package mysql

import (
	"encoding/json"
	"fmt"
	"time"
)

// toColumn converts the record's value into what the driver accepts.
//
// For the same reason as Postgres: the SDK's record is JSON, and an instant in
// it is an RFC 3339 STRING. The MySQL driver sends the string as text, and the
// server refuses with "Incorrect datetime value" -- or worse, accepts it and
// stores zero, depending on sql_mode.
//
//	column                  accepts
//	datetime/timestamp      RFC 3339 string, time.Time, or an epoch
//	date                    YYYY-MM-DD string or RFC 3339
//	decimal/numeric         string, preserved
//	json                    serialized
//	everything else         passes through
func toColumn(v any, typ string) (any, error) {
	if v == nil {
		return nil, nil
	}

	switch typ {
	case "datetime", "timestamp":
		return toInstant(v, typ)

	case "date":
		t, err := toInstant(v, typ)
		if err != nil {
			return nil, err
		}
		if ts, ok := t.(time.Time); ok {
			return ts.Format("2006-01-02"), nil
		}
		return t, nil

	case "json":
		switch t := v.(type) {
		case string:
			return t, nil
		case []byte:
			return string(t), nil
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("encoding as json: %w", err)
			}
			return string(b), nil
		}

	default:
		return v, nil
	}
}

func toInstant(v any, typ string) (any, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case string:
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339,
			"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02",
		} {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts.UTC(), nil
			}
		}
		return nil, fmt.Errorf("%q is not a timestamp this column (%s) accepts: use RFC 3339, "+
			"as in 2026-09-05T12:30:00Z", elide(t), typ)
	case float64:
		return time.Unix(int64(t), 0).UTC(), nil
	case int64:
		return time.Unix(t, 0).UTC(), nil
	case int:
		return time.Unix(int64(t), 0).UTC(), nil
	default:
		return nil, fmt.Errorf("a %T cannot go into a %s column", v, typ)
	}
}

func elide(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:37] + "…"
}
