package autotable

import (
	"sort"

	"github.com/AreteAcademy/brevis/sdk"
)

// columns gives each of the record's fields a column of its own.
//
// The rule, and there is no exception to it:
//
//	scalar          → STRING
//	object, array   → JSON
//
// **No inference, ever.** The reason is written in the SDK and it does not
// change here: deducing NUMERIC(18,2) from an encoding/json float64 is
// guessing, and a field that arrives whole today and fractional tomorrow would
// change the column with nobody writing anything. With everything STRING that
// failure cannot happen.
//
// `null` has no shape and becomes STRING. `true`/`false` are scalars and become
// STRING, which is the one that most invites an exception and does not get one.
//
// What it costs, said where somebody will read it: no partition pruning on a
// date inside the record, no numeric aggregation without a cast, and every
// query casting. Typing a column is the PROMOTION path -- a human writing it in
// the YAML, reviewed in a diff.
type columns struct{}

func (columns) name() string { return ShapeColumns }

// FieldName is what a record's field has to match to become a column.
//
// The SDK's, so a pipeline landing this shape refuses the same names with
// the same message.
var FieldName = sdk.LandingFieldName

// validate is the field-name rule, applied per event before anything is
// buffered. See the shaper interface for the batch this did not exist to
// protect.
func (c columns) validate(record map[string]any) error {
	return sdk.LandingFieldNames(record)
}

// columns renders the record's fields, each into a column of its own.
//
// The SDK's, so a pipeline landing this shape writes the same values. The
// rendering there is deliberately not this package's `Text`: these values go
// into tables, and the one in text.go is documented as free to get stricter.
func (c columns) columns(record map[string]any) (map[string]any, error) {
	return sdk.LandingSpread(record)
}

func (c columns) schema(record map[string]any) (sdk.Schema, error) {
	// Sorted, so the same record always declares the same DDL. Unsorted, two
	// runs over identical data would produce columns in different orders, and
	// a CREATE TABLE is easier to reason about when it does not move.
	if err := c.validate(record); err != nil {
		return nil, err
	}
	out := make(sdk.Schema, 0, len(record))
	for _, k := range sorted(record) {
		out = append(out, sdk.Column{Name: k, Type: typeOf(record[k])})
	}
	return out, nil
}

// typeOf is the whole of the type rule.
func typeOf(v any) sdk.ColumnType {
	switch v.(type) {
	case map[string]any, []any:
		return sdk.TypeJSON
	}
	return sdk.TypeString
}

func sorted(record map[string]any) []string {
	out := make([]string, 0, len(record))
	for k := range record {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
