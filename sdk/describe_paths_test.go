package sdk_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// describedPaths runs a pipeline under the engine and returns the paths its
// description carried, in order.
func describedPaths(t *testing.T, p sdk.Pipeline) []string {
	t.Helper()
	ev := sdk.DescribeForTest(&p)
	raw, err := json.Marshal(ev["fields"])
	if err != nil {
		t.Fatal(err)
	}
	var fields []struct{ Path string }
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range fields {
		paths = append(paths, f.Path)
	}
	return paths
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
	for name, p := range cases {
		seen := map[string]bool{}
		for _, path := range describedPaths(t, p) {
			if seen[path] {
				t.Errorf("%s: %q is described twice", name, path)
			}
			seen[path] = true
		}
		if name == "postgres with a declared schema" && !seen["target.declared.schema"] {
			t.Errorf("%s: the declared columns are not under target.declared: %v", name, keys(seen))
		}
	}
}

func keys(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ", ")
}
