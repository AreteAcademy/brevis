package autotable

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// The record is the consumer's own, from issue #40, and it is chosen rather
// than invented: a field called `data`, a numeric id that fmt.Sprint would
// turn into scientific notation, a decimal that arrives as float64, a nested
// object and an array. Every one of them is a way the two paths could drift.
func bacen() map[string]any {
	return map[string]any{
		"data":        "01/12/2025",
		"series":      float64(21129),
		"source_key":  "21129|01/12/2025",
		"valor":       8.89,
		"external_id": float64(26130000),
		"meta":        map[string]any{"uf": "SP", "fonte": "bacen"},
		"tags":        []any{"serie", "diaria"},
	}
}

const bothTable = "bronze_bacen"

// gatewayRow runs the gateway's whole path -- open, row, shape, group -- and
// not just the shaper. Exercising a function is not exercising the wire, and
// the wire is where the control columns are joined to the record's.
func gatewayRow(t *testing.T, shape string) map[string]any {
	t.Helper()
	r := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: shape,
		Into:  &gateway.Sink{Type: "probe"},
	})
	groups, _, err := r.group([]gateway.Envelope{{Payload: map[string]any{
		FieldTable:     bothTable,
		FieldUniqueKey: "source_key",
		FieldData:      bacen(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := groups[bothTable]
	if len(rows) != 1 {
		t.Fatalf("the batch grouped into %d rows", len(rows))
	}
	row, ok := rows[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("the payload is a %T", rows[0].Payload)
	}
	return row
}

// pipelineRow runs the SDK transformer a client would write.
func pipelineRow(t *testing.T, opts ...sdk.LandingArg) map[string]any {
	t.Helper()
	out, err := sdk.Landing(bothTable, append([]sdk.LandingArg{
		sdk.LandingKey("source_key"),
	}, opts...)...)(bacen())
	if err != nil {
		t.Fatal(err)
	}
	row, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Landing returned %T", out)
	}
	return row
}

// differing is the set of columns where the two rows disagree, by name.
func differing(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	var out []string
	for k := range seen {
		av, ap := a[k]
		bv, bp := b[k]
		if ap != bp || av != bv {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// One record, two paths, one table.
//
// This is the claim the whole landing layout rests on: a team can move a
// stream from the gateway to a pipeline, or run both at once, and read the
// result as one table. If the two disagree on a single value, they cannot.
func TestBothPathsLandTheSameRow(t *testing.T) {
	gw := gatewayRow(t, ShapeColumns)
	pipe := pipelineRow(t, sdk.LandingColumns())

	// The id first, because it is what makes the same record delivered twice
	// one row instead of two.
	if gw[ColumnID] != pipe[ColumnID] {
		t.Errorf("the two paths mint different ids:\n  gateway  %v\n  pipeline %v\n\n"+
			"The formula lives in the SDK and both call it. Two ids means one "+
			"of them is fingerprinting something other than the producer's "+
			"record -- and a merge would then duplicate every row.",
			gw[ColumnID], pipe[ColumnID])
	}

	// Every field column, by value. This is where one rendering gets proven:
	// 21129 must not be 2.1129e+04 on one side and "21129" on the other.
	for k, v := range bacen() {
		if gw[k] == nil {
			t.Errorf("the gateway has no %q column", k)
			continue
		}
		if gw[k] != pipe[k] {
			t.Errorf("%s:\n  gateway  %#v\n  pipeline %#v\n\n"+
				"The record was %#v. Both sides render through "+
				"sdk.LandingSpread, so a difference here means one of them "+
				"stopped.", k, gw[k], pipe[k], v)
		}
	}
	if gw[ColumnOperation] != pipe[ColumnOperation] {
		t.Errorf("operation: gateway %v, pipeline %v", gw[ColumnOperation], pipe[ColumnOperation])
	}
	if gw[ColumnRecordKey] != pipe[ColumnRecordKey] {
		t.Errorf("record key: gateway %v, pipeline %v", gw[ColumnRecordKey], pipe[ColumnRecordKey])
	}
}

// The columns shape must not introduce a divergence the document shape does
// not already have.
//
// There ARE differences, and they are legitimate: the gateway knows its own
// name and stream and a pipeline has neither, the two clocks tick at
// different instants, and the gateway measured the wire. What must not happen
// is a NEW one appearing because the record now sits in columns instead of a
// JSON blob.
func TestTheColumnsShapeAddsNoDivergence(t *testing.T) {
	// The columns the two paths are ALLOWED to disagree on, each with the
	// reason. Anything else showing up here is the finding.
	expected := map[string]string{
		ColumnStream:        "the gateway knows its stream; a pipeline has none",
		ColumnGateway:       "the gateway knows its own name; a pipeline has none",
		ColumnReceivedAt:    "two clocks, two instants -- the same value would mean one is frozen",
		ColumnReceivedBytes: "the gateway measured the WIRE, envelope included; a pipeline has no wire",
	}

	for _, c := range []struct {
		name string
		diff []string
	}{
		{"document", differing(gatewayRow(t, ShapeDocument), pipelineRow(t))},
		{"columns", differing(gatewayRow(t, ShapeColumns), pipelineRow(t, sdk.LandingColumns()))},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range c.diff {
				if why, ok := expected[k]; !ok {
					t.Errorf("the two paths disagree on %q, and nothing here "+
						"says they may. Either it is a drift, or it is "+
						"legitimate and belongs in the list above with its "+
						"reason", k)
				} else {
					t.Logf("%s differs, as expected: %s", k, why)
				}
			}
		})
	}
}

// Under the document shape the record is one JSON column, and the two paths
// have to agree on its BYTES -- not merely on its meaning. A column read with
// json_extract on one row and not the other is a table with two formats.
func TestBothPathsAgreeOnTheDocumentColumn(t *testing.T) {
	gw := gatewayRow(t, ShapeDocument)
	pipe := pipelineRow(t)
	if gw[ColumnData] != pipe[sdk.LandingColumnData] {
		t.Errorf("the `data` column differs:\n  gateway  %v\n  pipeline %v",
			gw[ColumnData], pipe[sdk.LandingColumnData])
	}
}

// Where the pipeline sends an explicit NULL and the gateway omits the column,
// that column must have no DEFAULT.
//
// This is the brevis_loaded_at bug written down so it cannot come back. An
// explicit NULL OVERRIDES a column's DEFAULT, so "absent" and "present and
// nil" are interchangeable only where there is nothing to override. The two
// paths represent "no value" differently -- the gateway omits, the pipeline
// sends nil -- and that is fine exactly as long as this holds.
//
// The unit test that first covered loaded_at asserted "present and nil",
// which IS the bug: it looked at the map instead of the table, and a map is
// not a table.
func TestAnExplicitNullOnlyGoesWhereThereIsNoDefault(t *testing.T) {
	gw := gatewayRow(t, ShapeColumns)
	pipe := pipelineRow(t, sdk.LandingColumns())

	hasDefault := map[string]bool{}
	for _, c := range sdk.LandingControlColumns(sdk.LandingOptions{}) {
		if c.Default != nil {
			hasDefault[c.Name] = true
		}
	}
	// If nothing in the layout has a DEFAULT any more, this test proves
	// nothing and must say so rather than pass quietly.
	if len(hasDefault) == 0 {
		t.Fatal("no control column declares a DEFAULT: either the layout " +
			"changed or this stopped reading it, and either way the check " +
			"below can no longer fail")
	}

	for k, v := range pipe {
		if v != nil {
			continue
		}
		if _, sent := gw[k]; sent {
			continue
		}
		if hasDefault[k] {
			t.Errorf("the pipeline sends %s as an explicit NULL where the "+
				"gateway omits it -- and that column has a DEFAULT, which an "+
				"explicit NULL overrides. The same column would then hold a "+
				"timestamp on the gateway's rows and NULL on the pipeline's", k)
		}
	}
	// And neither path sends the one column that does have a DEFAULT.
	for _, row := range []struct {
		name string
		m    map[string]any
	}{{"gateway", gw}, {"pipeline", pipe}} {
		if v, sent := row.m[ColumnLoadedAt]; sent {
			t.Errorf("the %s row carries %s as %v: the destination's DEFAULT "+
				"stamps it, and sending anything -- NULL included -- costs "+
				"the one end-to-end latency measurement the pair exists for",
				row.name, ColumnLoadedAt, v)
		}
	}
}

// The two paths must declare the same TYPES, not only carry the same values.
//
// This is the half TestBothPathsLandTheSameRow cannot see. It compares the
// ROW, and the row is right: both sides put `{"uf":"SP"}` in `meta`. What it
// never asks is what COLUMN that value goes into — and there the two used to
// disagree completely.
//
// The gateway types from the RAW record and renders separately. A pipeline
// renders first, in the transformer, and the driver only ever sees the
// rendered row: by then an object has become text and the shape is gone. So
// the gateway declared `meta JSON` and a pipeline declared `meta STRING`.
//
// On a SHARED table that is not a cosmetic difference, it is a wall: the
// gateway creates the column JSON, the pipeline rediscovers it as string, and
// the plan refuses — "meta is json in the table and string in the
// declaration". A pipeline could not write into a gateway's `shape: columns`
// table, which is the one interoperability this layout exists for.
func TestBothPathsDeclareTheSameTypes(t *testing.T) {
	record := bacen()

	fromGateway, err := columns{}.schema(record)
	if err != nil {
		t.Fatal(err)
	}

	// What a pipeline declares: over the row its transformer produced, which
	// is the only thing its driver ever sees.
	out, err := sdk.Landing(bothTable,
		sdk.LandingKey("source_key"), sdk.LandingColumns())(record)
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)
	// The layout's own columns are declared, not discovered; drop them.
	producer := map[string]any{}
	for k, v := range row {
		if !strings.HasPrefix(k, sdk.LandingPrefix) {
			producer[k] = v
		}
	}
	fromPipeline, err := sdk.LandingSchemaOf(producer)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]sdk.ColumnType{}
	for _, c := range fromPipeline {
		byName[c.Name] = c.Type
	}
	for _, c := range fromGateway {
		got, present := byName[c.Name]
		if !present {
			t.Errorf("the gateway declares %q and a pipeline does not", c.Name)
			continue
		}
		if got != c.Type {
			t.Errorf("%s: the gateway declares %s and a pipeline declares %s\n\n"+
				"Same record, same layout, two tables. On a shared one the "+
				"second writer is refused outright: `%s is %s in the table and "+
				"%s in the declaration`.",
				c.Name, c.Type, got, c.Name, c.Type, got)
		}
	}
	if len(fromGateway) != len(byName) {
		t.Errorf("%d columns from the gateway, %d from the pipeline",
			len(fromGateway), len(byName))
	}
}

// Neither shape hands a plain string to a column it declared JSON.
//
// From sdk v0.74.0 the BigQuery driver REFUSES that, so a shape that did it
// would fail its own writes. Before that version it did something worse: the
// write succeeded and the column held a JSON value of TYPE string, where
// every JSON_VALUE returns NULL. A consumer found it across 23 columns in 9
// tables, in production.
//
// It asserts the Go type rather than the wire, because the wire is the SDK's
// to encode and the SDK tests it there. What belongs here is that this
// package never hands over the one thing that cannot work.
func TestNeitherShapeHandsAStringToAJSONColumn(t *testing.T) {
	record := bacen()

	for _, sh := range []shaper{document{}, columns{}} {
		t.Run(sh.name(), func(t *testing.T) {
			declared, err := sh.schema(record)
			if err != nil {
				t.Fatal(err)
			}
			values, err := sh.columns(record)
			if err != nil {
				t.Fatal(err)
			}

			var sawJSON bool
			for _, c := range declared {
				if c.Type != sdk.TypeJSON {
					continue
				}
				sawJSON = true
				switch v := values[c.Name].(type) {
				case string, []byte:
					t.Errorf("%s is declared JSON and the shape hands over a "+
						"%T: %v\n\nFrom sdk v0.74.0 the BigQuery driver "+
						"refuses this outright. Before it, the write succeeded "+
						"and JSON_VALUE against the column returned NULL.",
						c.Name, v, v)
				}
			}
			if !sawJSON {
				t.Fatalf("%s declared no JSON column for a record with an "+
					"object and an array in it: this test is not looking at "+
					"what it claims to", sh.name())
			}
		})
	}
}

// The reserved rule follows BREVIS_LANDING_PREFIX here too.
//
// A producer who can forge a control column forges a real-looking one, and
// `open` refuses the prefix before anything reads the record. Configure the
// prefix and that rule has to move with it: under `acme_`, `acme_region` is
// reserved and `brevis_region` is an ordinary field.
//
// In a child process, because the prefix is resolved when the package loads —
// and because with the variable unset "the literal brevis_" and "the
// configured prefix" are the same string, so a refusal hard-coded to the
// literal passes everything written in-process.
func TestTheReservedRuleFollowsThePrefix(t *testing.T) {
	const marker = "BREVIS_TEST_GW_RESERVED_CHILD"

	if os.Getenv(marker) == "1" {
		if Prefix != "acme_" {
			t.Fatalf("Prefix = %q, want acme_", Prefix)
		}

		_, err := open(map[string]any{
			FieldTable: "t",
			FieldData:  map[string]any{"id": "A", "acme_region": "SP"},
		})
		if err == nil {
			t.Fatal("a record carrying acme_region was accepted: the producer " +
				"just forged a control column")
		}
		if !strings.Contains(err.Error(), "acme_region") ||
			!strings.Contains(err.Error(), "acme_") {
			t.Errorf("the refusal does not name the field and the prefix: %v", err)
		}

		env, err := open(map[string]any{
			FieldTable: "t",
			FieldData:  map[string]any{"id": "A", "brevis_region": "SP"},
		})
		if err != nil {
			t.Fatalf("brevis_region was refused under a different prefix: %v", err)
		}
		if env.record["brevis_region"] != "SP" {
			t.Errorf("brevis_region did not survive as an ordinary field: %v", env.record)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestTheReservedRuleFollowsThePrefix", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_LANDING_PREFIX=acme")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_LANDING_PREFIX=acme:\n%s", out)
	}
}

// Flattening cannot forge a control column, and it is refused PER EVENT.
//
// `open` checks the record's own keys, and `brevis` does not start with
// `brevis_` — so with BREVIS_NORMALIZE_DATA on, `{"brevis": {"stream": "x"}}`
// walks past it and becomes `brevis_stream`. A forged control column is worse
// than a missing one, because it looks real.
//
// Admit is where it has to be caught. The alternative is a whole batch in the
// dead letter for one producer's field, which is the poison batch this
// package had once: four events answered 202 and all four buried, three of
// them belonging to producers who did nothing wrong.
func TestFlatteningCannotForgeAControlColumn(t *testing.T) {
	const marker = "BREVIS_TEST_GW_FORGE_CHILD"

	if os.Getenv(marker) == "1" {
		r := build(t, gateway.Sink{
			Type:  gateway.SinkAutoTable,
			Shape: ShapeColumns,
			Into:  &gateway.Sink{Type: "probe"},
		})

		err := r.Admit(map[string]any{
			FieldTable: "t_forge",
			FieldData: map[string]any{
				"id": "A", "brevis": map[string]any{"stream": "not-a-real-gateway"},
			},
		})
		if err == nil {
			t.Fatal("Admit accepted it: the event is buffered, and at write " +
				"time the producer has forged a control column — or buried " +
				"the batch around it")
		}
		if !strings.Contains(err.Error(), "brevis_stream") ||
			!strings.Contains(err.Error(), "brevis.stream") {
			t.Errorf("the refusal does not name both the column and the field "+
				"it came from: %v", err)
		}

		// And an ordinary nested field still gets through.
		if err := r.Admit(map[string]any{
			FieldTable: "t_forge",
			FieldData:  map[string]any{"id": "A", "name": map[string]any{"first": "x"}},
		}); err != nil {
			t.Errorf("an ordinary nested field was refused: %v", err)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestFlatteningCannotForgeAControlColumn", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_NORMALIZE_DATA=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_NORMALIZE_DATA=true:\n%s", out)
	}
}

// And the two paths still declare the same columns with flattening on.
func TestBothPathsFlattenTheSameWay(t *testing.T) {
	const marker = "BREVIS_TEST_GW_FLAT_CHILD"

	if os.Getenv(marker) == "1" {
		record := map[string]any{
			"source_key": "k-1",
			"name":       map[string]any{"first": "Coleen", "last": "Volk"},
		}

		fromGateway, err := columns{}.schema(record)
		if err != nil {
			t.Fatal(err)
		}
		out, err := sdk.Landing("t", sdk.LandingKey("source_key"), sdk.LandingColumns())(record)
		if err != nil {
			t.Fatal(err)
		}
		row := out.(map[string]any)

		for _, c := range fromGateway {
			if _, present := row[c.Name]; !present {
				t.Errorf("the gateway declares %q and a pipeline does not "+
					"produce it: %v", c.Name, row)
			}
		}
		if _, ok := row["name_first"]; !ok {
			t.Errorf("the pipeline did not flatten: %v", row)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestBothPathsFlattenTheSameWay", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_NORMALIZE_DATA=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("with BREVIS_NORMALIZE_DATA=true:\n%s", out)
	}
}
