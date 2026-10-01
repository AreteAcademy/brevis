package core

import (
	"fmt"
	"sort"
	"strings"
)

// FlattenOneLevel gives a nested object's fields columns of their own, one
// level deep, the way pandas' json_normalize does.
//
//	{"id": 1, "name": {"first": "Coleen", "last": "Volk"}}
//	→ id, name_first, name_last
//
// ONE LEVEL. A deeper object is a VALUE and not a longer path:
// `{"a": {"b": {"c": 1}}}` is `a_b` holding `{"c": 1}`, which becomes a JSON
// column. An array is a value too, always: a record that wants one row per
// element wants ArrayAt, which is a different operation with a different
// name.
//
// Joined with `_` and lower-cased, and NOT split on camelCase. `userName`
// becomes `username` and not `user_name`, because the other reading has no
// agreed answer for acronyms -- `HTTPStatus`, `userID` -- and every library
// disagrees about them. Decided once, because going from one to the other
// later renames every camelCase column in every table.
//
// A FIELD THE PRODUCER SENT NEVER VANISHES. An empty object has nothing to
// flatten and keeps its own column, holding `{}`: dropping it would take a
// field out of the table with nothing saying so.
//
// A field that is an object in one record and a scalar in another produces
// BOTH -- `name_first` from the one and `name` from the other. That is the
// example's own answer and it is pandas' too.
func FlattenOneLevel(record map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(record))
	// What produced each column, so a collision can name both sides rather
	// than say that one happened.
	from := make(map[string]string, len(record))

	// Sorted, so the FIRST source of a collision is the same one on every
	// run. Map iteration is randomised, and a refusal that names a different
	// pair each time is a refusal nobody can act on.
	keys := make([]string, 0, len(record))
	for k := range record {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	put := func(column, source string, v any) error {
		if !ColumnName.MatchString(column) {
			return fmt.Errorf("flattening %q would make the column %q, which "+
				"cannot be a column name: it has to match %s. That is "+
				"BigQuery's rule and it is the narrowest of the four",
				source, column, ColumnName)
		}
		if was, taken := from[column]; taken {
			return fmt.Errorf("%q and %q both become the column %q. Go's map "+
				"iteration is randomised, so letting one win would give this "+
				"batch a different table on a different run. Rename one of "+
				"them before it reaches the landing layout",
				was, source, column)
		}
		from[column] = source
		out[column] = v
		return nil
	}

	for _, k := range keys {
		nested, isObject := record[k].(map[string]any)
		if !isObject || len(nested) == 0 {
			// A scalar, an array, a null -- and an EMPTY object, which has
			// nothing to flatten and keeps its own column.
			if err := put(strings.ToLower(k), k, record[k]); err != nil {
				return nil, err
			}
			continue
		}

		subs := make([]string, 0, len(nested))
		for sk := range nested {
			subs = append(subs, sk)
		}
		sort.Strings(subs)
		for _, sk := range subs {
			source := k + "." + sk
			if err := put(strings.ToLower(k+"_"+sk), source, nested[sk]); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
