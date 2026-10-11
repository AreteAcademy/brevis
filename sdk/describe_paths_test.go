package sdk_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// describedValues is a pipeline's description, by path, as the engine reads
// it.
func describedValues(t *testing.T, p sdk.Pipeline) map[string]any {
	t.Helper()
	ev := sdk.DescribeForTest(&p)
	raw, err := json.Marshal(ev["fields"])
	if err != nil {
		t.Fatal(err)
	}
	var fields []struct {
		Path  string
		Value any
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for _, f := range fields {
		out[f.Path] = f.Value
	}
	return out
}

// The Target's own declaration and the writer's settings live under one
// prefix, and the engine refuses a description that names a path twice --
// so a Postgres table with a declared Schema used to lose its whole
// configuration: the declaration's `target.schema` (its columns) and the
// driver's `target.schema` (the Postgres schema) collided.
func TestTheDeclarationAndTheWriterNeverShareAPath(t *testing.T) {
	cases := map[string]sdk.Pipeline{
		"postgres with a declared schema": {
			Source: sdk.Source{From: from.Files{Path: "./in/"}},
			Target: sdk.Target{
				To:     postgres.Table{DSN: "postgres://u:p@db/shop", Name: "landing.orders"},
				Schema: sdk.Schema{{Name: "id", Type: sdk.TypeString}},
			},
		},
		"files with a partition": {
			Source: sdk.Source{From: from.Files{Path: "./in/"}},
			Target: sdk.Target{
				To:          to.Files{Path: "gs://acme/out/", PartitionBy: "dt"},
				PartitionBy: "loaded_at",
			},
		},
	}
	// The VALUES, on both sides: describe() keeps the first of a repeated
	// path, so "no path appears twice" holds whatever the paths are. What
	// shows the collision is a value that went missing.
	want := map[string]map[string]any{
		"postgres with a declared schema": {
			"target.schema":          "landing",
			"target.declared.schema": []any{"id string"},
		},
		"files with a partition": {
			"target.partition_by":          "dt",
			"target.declared.partition_by": "loaded_at",
		},
	}
	for name, p := range cases {
		got := describedValues(t, p)
		for path, v := range want[name] {
			if !reflect.DeepEqual(got[path], v) {
				t.Errorf("%s: %s = %#v, want %#v", name, path, got[path], v)
			}
		}
	}
}
