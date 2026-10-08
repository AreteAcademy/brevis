package core

import (
	"fmt"
	"sort"
)

// CheckJSONColumns refuses a string of JSON where a JSON value belongs.
//
// BigQuery's refusal, and not a rule about JSON columns in general. There the
// row is marshalled whole, so a Go string becomes a JSON string literal and
// the column ends up holding a JSON value of TYPE string: JSON_TYPE returns
// "string" and every JSON_VALUE against it returns NULL. The column is the
// right type and the data in it is unreachable, which is the worst of the
// three outcomes because it looks correct -- a consumer found it in
// production, across 23 columns, rather than in any test.
//
// Postgres and MySQL parse a string into their JSON column and always have.
// They are not changed and must not be: refusing there would break loads that
// are correct today.
//
// EXACT TYPES, not reflect.Kind, and that is load-bearing. json.RawMessage is
// the standard library's own answer to this problem and JSONText is ours;
// neither matches `case string` or `case []byte`, and both marshal to the JSON
// they hold. A check written on Kind would refuse the two correct answers.
// IT TAKES THE WHOLE DECLARATION. [#43] The row is matched to the column
// under the name the DESTINATION stores it: on BigQuery a row carrying
// `payload` answers for a declared `Payload`, and asking by exact name let a
// string into a JSON column without a word -- which is the exact outcome
// this check exists to stop, reached through the one gap it had.
func CheckJSONColumns(opt WriteOptions, records []Envelope) error {
	declared := opt.Schema
	if len(declared) == 0 || len(records) == 0 {
		return nil
	}

	// Folded name -> the name the DECLARATION uses, which is the one the
	// refusal has to print: the consumer fixes their Schema, not the fold.
	jsonColumn := make(map[string]string, len(declared))
	for _, c := range declared {
		if c.Type == TypeJSON {
			jsonColumn[opt.ColumnKey(c.Name)] = c.Name
		}
	}
	if len(jsonColumn) == 0 {
		return nil
	}

	// The whole batch, not the first record. CheckRow samples records[0]
	// because every record comes out of one Transform chain and the SHAPE of
	// the chain is the same for all of them; a VALUE is not. A string in
	// record 50 lands just as silently as one in record 0.
	bad := map[string]string{}
	for _, e := range records {
		row, err := AsObject(e.Payload)
		if err != nil {
			continue // EncodeRows refuses anything that is not an object
		}
		for field, value := range row {
			name, declaredJSON := jsonColumn[opt.ColumnKey(field)]
			if !declaredJSON {
				continue
			}
			if _, seen := bad[name]; seen {
				continue
			}
			switch value.(type) {
			case string:
				bad[name] = "a string"
			case []byte:
				bad[name] = "a []byte"
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}

	names := make([]string, 0, len(bad))
	for n := range bad {
		names = append(names, n)
	}
	sort.Strings(names)

	first := names[0]
	return fmt.Errorf("column %q is declared JSON and the row carries %s. "+
		"BigQuery would store that as a JSON value of TYPE string: JSON_TYPE "+
		"returns \"string\" and every JSON_VALUE against it returns NULL, "+
		"without an error. Pass the object or the array itself, or "+
		"sdk.JSONText(s) when the text already IS JSON. Columns: %v",
		first, bad[first], names)
}
