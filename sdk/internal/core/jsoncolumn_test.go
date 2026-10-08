package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// A column declared JSON must not receive a string of JSON.
//
// On BigQuery the row is marshalled whole, so a Go string becomes a JSON
// string literal and the column holds a JSON value of TYPE string. JSON_TYPE
// returns "string" and every JSON_VALUE against it returns NULL — silently,
// which is why a consumer found it in production rather than in a test.
//
// Postgres and MySQL parse a string into their JSON column and always have,
// so this is BigQuery's refusal and not a rule about JSON columns in general.
func TestAJSONColumnRefusesAStringOfJSON(t *testing.T) {
	declared := Schema{
		{Name: "id", Type: TypeString},
		{Name: "payload", Type: TypeJSON},
	}

	for _, bad := range []struct {
		name string
		v    any
	}{
		{"string", `{"a":1}`},
		{"[]byte", []byte(`{"a":1}`)},
	} {
		t.Run("refuses "+bad.name, func(t *testing.T) {
			err := CheckJSONColumns(WriteOptions{Schema: declared}, batch(map[string]any{
				"id": "1", "payload": bad.v,
			}))
			if err == nil {
				t.Fatal("accepted: the column would hold a JSON string and " +
					"every JSON_VALUE against it would be NULL")
			}
			if !strings.Contains(err.Error(), "payload") {
				t.Errorf("the refusal does not name the column: %v", err)
			}
			if !strings.Contains(err.Error(), "JSONText") {
				t.Errorf("the refusal does not say how to pass text that IS "+
					"already JSON: %v", err)
			}
		})
	}

	// What it must NOT refuse. The two named types are the correct ways to
	// hand over JSON, and neither matches an exact `case string` or
	// `case []byte` — which is the whole reason the rule is written as a type
	// switch and not as a reflect.Kind check. A "simplification" to Kind would
	// break json.RawMessage, the stdlib's own answer to this problem.
	for _, ok := range []struct {
		name string
		v    any
	}{
		{"JSONText", JSONText(`{"a":1}`)},
		{"json.RawMessage", json.RawMessage(`{"a":1}`)},
		{"map", map[string]any{"a": 1}},
		{"slice", []any{1, 2}},
		{"nil", nil},
		{"number", float64(1)},
		{"bool", true},
	} {
		t.Run("allows "+ok.name, func(t *testing.T) {
			if err := CheckJSONColumns(WriteOptions{Schema: declared}, batch(map[string]any{
				"id": "1", "payload": ok.v,
			})); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}

	// And a string in a column that is a STRING is exactly right.
	if err := CheckJSONColumns(WriteOptions{Schema: declared}, batch(map[string]any{
		"id": "1", "payload": JSONText("{}"),
	})); err != nil {
		t.Errorf("a string in a string column was refused: %v", err)
	}

	// Nothing declared, nothing checked — the rule the rest of the struct
	// follows.
	if err := CheckJSONColumns(WriteOptions{}, batch(map[string]any{"payload": `{"a":1}`})); err != nil {
		t.Errorf("with no declaration there is nothing to check: %v", err)
	}

	// The whole batch, not the first record: a string in record 50 lands just
	// as silently as one in record 0.
	records := make([]map[string]any, 50)
	for i := range records {
		records[i] = map[string]any{"id": "x", "payload": JSONText("{}")}
	}
	records[49]["payload"] = `{"late":true}`
	if err := CheckJSONColumns(WriteOptions{Schema: declared}, batch(records...)); err == nil {
		t.Error("a string carried only by the last record was accepted")
	}
}
