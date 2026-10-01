package sdk

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/load"
)

// A column declared JSON receives a JSON VALUE, not a string of one.
//
// This is the test that was missing when a consumer reported gateway v0.15.0
// landing every object and array in BigQuery as a JSON string: JSON_TYPE
// returned "string" and every JSON_VALUE returned NULL, across 23 columns in
// 9 tables. The column was the right type and the data in it was unreachable,
// which is the worst of the three outcomes because it looks correct.
//
// IT ASSERTS THE BYTES, and that is the whole point. A check that the value
// "is not a Go string" passes for any type whose MarshalJSON is wrong, and
// what BigQuery reads is the line, not the type. encodeRows was a method on a
// *Loader it never used, so nothing could reach these bytes without a client;
// it is a pure function now.
//
// NOT testable through the emulator: floci refuses load jobs with a 405, so
// "write a row and read JSON_TYPE back" cannot run there. The wire is where
// this is decidable without a warehouse.
func TestAJSONColumnReceivesJSONAndNotAStringOfIt(t *testing.T) {
	const table = "bronze.repro"
	record := map[string]any{
		"id":    "1",
		"obj":   map[string]any{"a": float64(1)},
		"arr":   []any{float64(1), float64(2)},
		"texto": `{"nao":"sou json"}`,
	}

	for _, c := range []struct {
		shape   string
		opts    []LandingArg
		declare func(map[string]any) Schema
	}{
		{
			// Each field its own column; `obj` and `arr` are JSON.
			shape: "columns",
			opts:  []LandingArg{LandingColumns()},
			declare: func(row map[string]any) Schema {
				producer := map[string]any{}
				for k, v := range row {
					if !strings.HasPrefix(k, LandingPrefix) {
						producer[k] = v
					}
				}
				s, err := LandingSchemaOf(producer)
				if err != nil {
					t.Fatal(err)
				}
				return append(LandingControlColumns(LandingOptions{}), s...)
			},
		},
		{
			// The record whole, in `data` -- which LandingDataColumn declares
			// JSON. This is the DEFAULT shape, and the consumer did not report
			// it because they do not use it.
			shape:   "document",
			opts:    nil,
			declare: func(map[string]any) Schema { return LandingSchema(LandingOptions{}) },
		},
	} {
		t.Run(c.shape, func(t *testing.T) {
			out, err := Landing(table, append([]LandingArg{LandingKey("id")}, c.opts...)...)(record)
			if err != nil {
				t.Fatal(err)
			}
			row := out.(map[string]any)

			line, err := load.EncodeRows([]Envelope{{Payload: row}})
			if err != nil {
				t.Fatal(err)
			}
			var encoded map[string]json.RawMessage
			if err := json.Unmarshal(line, &encoded); err != nil {
				t.Fatalf("the line is not a JSON object: %s", line)
			}

			var sawJSON, sawString bool
			for _, col := range c.declare(row) {
				raw, present := encoded[col.Name]
				if !present {
					continue // brevis_loaded_at is the destination's
				}
				switch col.Type {
				case TypeJSON:
					sawJSON = true
					if len(raw) > 0 && raw[0] == '"' {
						t.Errorf("%s is declared JSON and goes on the wire as a "+
							"STRING: %s\n\n"+
							"BigQuery stores that as a JSON value of type "+
							"`string`. JSON_TYPE returns \"string\" and every "+
							"JSON_VALUE(%s, '$.x') returns NULL. The object has "+
							"been encoded twice.", col.Name, raw, col.Name)
					}
				case TypeString:
					// NULL is a legitimate encoding for a string column, and
					// three of the layout's own are NULL from a pipeline --
					// brevis_stream, brevis_gateway and received_bytes. Only a
					// value says anything here.
					if string(raw) == "null" {
						continue
					}
					sawString = true
					// The inverse, or the fix would make every text field an
					// object: a producer's string that LOOKS like JSON is
					// still a string.
					if len(raw) > 0 && raw[0] != '"' {
						t.Errorf("%s is declared STRING and goes on the wire "+
							"unquoted: %s", col.Name, raw)
					}
				}
			}
			// A run that checked nothing passes, and would pass forever.
			if !sawJSON || !sawString {
				t.Fatalf("the shape declared no JSON column (%v) or no string "+
					"column (%v): this test is not looking at what it claims to",
					sawJSON, sawString)
			}
		})
	}
}
