package autotable

import (
	"encoding/json"
	"fmt"

	"github.com/AreteAcademy/brevis/sdk"
)

// The two shapes a record can take in the table.
//
// One contract for the producer either way -- the envelope never changes. What
// changes is what the operator's table looks like, which is their decision and
// not the producer's.
const (
	// ShapeDocument puts the whole record in one JSON column. No DDL ever
	// after the create: a new field is a new key, so there is no
	// schema-change quota, no write-stream reopen and no race between
	// replicas.
	ShapeDocument = "document"

	// ShapeColumns gives each of the record's fields a column: scalars STRING,
	// objects and arrays JSON. It is the shape people picture, and it pays for
	// itself with everything ShapeDocument avoids -- see the DDL state machine.
	ShapeColumns = "columns"
)

// DefaultShape is the cheap one.
const DefaultShape = ShapeDocument

// ColumnData is where the record goes under ShapeDocument.
const ColumnData = "data"

// shaper turns a record into the columns it contributes, and declares them.
type shaper interface {
	// columns is what this record adds to the row.
	columns(record map[string]any) (map[string]any, error)

	// schema is what those columns are declared as, for a table being created.
	schema(record map[string]any) (sdk.Schema, error)

	name() string
}

func shaperFor(shape string) (shaper, error) {
	switch shape {
	case "", ShapeDocument:
		return document{}, nil
	case ShapeColumns:
		return columns{}, nil
	default:
		return nil, fmt.Errorf("`shape` is %q (use %s or %s)",
			shape, ShapeDocument, ShapeColumns)
	}
}

// document is one JSON column holding the record whole.
type document struct{}

func (document) name() string { return ShapeDocument }

func (document) columns(record map[string]any) (map[string]any, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("the record is not JSON: %w", err)
	}
	return map[string]any{ColumnData: string(body)}, nil
}

func (document) schema(map[string]any) (sdk.Schema, error) {
	// The same one column whatever the record holds, which is what makes a
	// table created here a template rather than a decision.
	return sdk.Schema{{Name: ColumnData, Type: sdk.TypeJSON}}, nil
}
