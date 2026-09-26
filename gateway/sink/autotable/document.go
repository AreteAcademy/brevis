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
	// validate refuses a record this shape cannot turn into columns, and it
	// exists to be called PER EVENT, before anything is buffered.
	//
	// Without it, `columns` and `schema` are the first things to see a record
	// -- and both run at WRITE time, on a whole batch. One field called
	// `my-field` was accepted with 202 and then failed the batch around it:
	// three well-formed events from three other producers went to the dead
	// letter for a fourth producer's mistake, and all four had been told 202.
	//
	// That is the poison batch the Admitter was built against. Only the table
	// name had been moved behind it; the field names had not.
	validate(record map[string]any) error

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

// validate accepts every record, because this shape has no field names to
// refuse: the whole record becomes ONE column whatever it holds. A key that
// could never be a column name here is just a key in a JSON document.
//
// The marshal in `columns` is the only other thing that could fail, and it
// cannot: the record came out of encoding/json, so it goes back in.
func (document) validate(map[string]any) error { return nil }

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
