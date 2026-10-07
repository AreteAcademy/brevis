package sdk

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// With BREVIS_NORMALIZE_DATA on, the layout flattens one level.
//
// In a child process, because the variable resolves when the package loads.
func TestTheLayoutFlattensWhenAsked(t *testing.T) {
	const marker = "BREVIS_TEST_FLATTEN_CHILD"

	if os.Getenv(marker) == "1" {
		record := map[string]any{
			"id":   float64(1),
			"name": map[string]any{"first": "Coleen", "last": "Volk"},
		}

		// The ROW and the SCHEMA have to agree about what the record
		// contributes. They are two functions over one record, and a table
		// declared with one set of names and filled with another fails at
		// the destination.
		row, err := LandingSpread(record)
		if err != nil {
			t.Fatal(err)
		}
		schema, err := LandingSchemaOf(record)
		if err != nil {
			t.Fatal(err)
		}
		if len(row) != len(schema) {
			t.Fatalf("the row has %d columns and the schema declares %d:\n"+
				"  row    %v\n  schema %v", len(row), len(schema), row, schema.Names())
		}
		for _, c := range schema {
			if _, present := row[c.Name]; !present {
				t.Errorf("the schema declares %q and the row does not carry it", c.Name)
			}
		}

		for _, want := range []string{"id", "name_first", "name_last"} {
			if _, present := row[want]; !present {
				t.Errorf("no %q column: %v", want, row)
			}
		}
		if _, flat := row["name"]; flat {
			t.Error("`name` survived whole beside its flattened fields, for a " +
				"record where it is only an object")
		}

		// THE ID DOES NOT MOVE. It is computed over the PRODUCER's record,
		// before any rendering — so flattening cannot reach it. If it could,
		// the same record with the flag on and off would get two ids and a
		// merge would duplicate the table.
		id, err := LandingID("landing.orders", "A-1", map[string]any{
			"id": "A-1", "total": 15.5,
			"tags": []any{"x", "y"}, "nested": map[string]any{"b": 2, "a": 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if id != "cf239eb8-b105-5bdd-98a2-7d25cf776170" {
			t.Errorf("LandingID = %s with flattening on: the id followed the "+
				"rendering, so the same record lands twice under two ids", id)
		}

		// And the TRANSFORMER mints it over the raw record too, which the
		// line above does not say: it pins the function, and a mutation that
		// flattens before calling it survived until this existed.
		nested := map[string]any{"id": "A-1", "name": map[string]any{"first": "x"}}
		want, err := LandingID("t", "A-1", nested)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Landing("t", LandingKey("id"), LandingColumns())(nested)
		if err != nil {
			t.Fatal(err)
		}
		if got := out.(map[string]any)[LandingColumnID]; got != want {
			t.Errorf("the row's id is %v and the record's is %v: the id was "+
				"minted over the FLATTENED record, so the same record gets one "+
				"id with the flag on and another with it off, and a merge "+
				"duplicates every row", got, want)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestTheLayoutFlattensWhenAsked", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_NORMALIZE_DATA=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_NORMALIZE_DATA=true:\n%s", out)
	}
}

// FLATTENING FORGES A CONTROL COLUMN, and the raw-key check does not see it.
//
// A producer sending `{"brevis": {"stream": "x"}}` passes the reserved check
// on the record's own keys — `brevis` does not start with `brevis_` — and
// flattening then joins them into `brevis_stream`. A forged control column is
// worse than a missing one, because it looks real.
//
// So the check has to run on the names the record would PRODUCE.
func TestFlatteningCannotForgeAControlColumn(t *testing.T) {
	const marker = "BREVIS_TEST_FORGE_CHILD"

	if os.Getenv(marker) == "1" {
		forged := map[string]any{
			"id":     "A",
			"brevis": map[string]any{"stream": "not-a-real-gateway"},
		}

		err := LandingFieldNames(forged)
		if err == nil {
			t.Fatal("the record was accepted: flattening it produces " +
				"`brevis_stream`, and the producer just forged a control column")
		}
		if !strings.Contains(err.Error(), "brevis_stream") {
			t.Errorf("the refusal does not name the column it would make: %v", err)
		}
		if !strings.Contains(err.Error(), "brevis.stream") {
			t.Errorf("the refusal does not name where it came from, so the "+
				"producer cannot find the field to rename: %v", err)
		}

		// And the whole path refuses it, not only the name check.
		if _, err := LandingSpread(forged); err == nil {
			t.Error("LandingSpread rendered the forged column")
		}
		if _, err := Landing("t", LandingKey("id"), LandingColumns())(forged); err == nil {
			t.Error("the transformer composed a row with the forged column")
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestFlatteningCannotForgeAControlColumn", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_NORMALIZE_DATA=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_NORMALIZE_DATA=true:\n%s", out)
	}
}
