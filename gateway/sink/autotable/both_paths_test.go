package autotable

import (
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
