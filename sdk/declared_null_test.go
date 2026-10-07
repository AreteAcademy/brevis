package sdk

import (
	"strings"
	"testing"
)

// A DECLARED column that arrives null stays in the row.
//
// The regression in v0.77.0, reported on #42 and reproduced against the
// published artifact before this test was written. A consumer's fetchers land
// with a declared schema:
//
//	Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())},
//	Target:    sdk.Target{Schema: append(sdk.LandingControlColumns(...), fields...), ...}
//
// `Target.declaredColumns()` turns that Schema into Columns, and every driver
// then runs `CheckRow`, which refuses a declared column the row does not have:
//
//	the Columns declaration lists Area_Drenagem, which the row does not have
//
// It hit 5 of their 23 fetchers. In each one a vendor field that is often null
// is declared, and on v0.76.0 it reached the row as nil and loaded as NULL.
//
// THE ROOT CAUSE IS NOT CheckRow. v0.77.0 made the row's SHAPE depend on the
// record's VALUES, and every check downstream is built on the premise that it
// does not -- `Discovered` says so in its own comment. The type is what must
// not read a null; the row was never the problem.
func TestADeclaredNullStaysInTheRow(t *testing.T) {
	row, err := LandingSpread(map[string]any{
		"source_key": "k-1", "Area_Drenagem": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, present := row["Area_Drenagem"]
	if !present {
		t.Fatalf("the row dropped Area_Drenagem: a declared column absent from "+
			"the row is refused by CheckRow in five drivers, which is the "+
			"v0.77.0 regression. The row has: %v", row)
	}
	if v != nil {
		t.Errorf("Area_Drenagem is %#v, want nil: null and \"\" are different "+
			"facts and the column cannot tell them apart afterwards", v)
	}
}

// And the schema still declares nothing for it, which is the #42 fix.
//
// This is the pair that has to hold at once, and it is the whole design: the
// ROW carries what the record carried, and the SCHEMA carries only what has a
// shape. Fixing one by breaking the other is what v0.77.0 did.
func TestANullIsInTheRowAndNotInTheSchema(t *testing.T) {
	record := map[string]any{"source_key": "k-1", "Area_Drenagem": nil}

	row, err := LandingSpread(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := row["Area_Drenagem"]; !present {
		t.Error("the row does not carry the null")
	}

	schema, err := LandingSchemaOf(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range schema {
		if c.Name == "Area_Drenagem" {
			t.Fatalf("the schema declares Area_Drenagem as %s from a null: a "+
				"null has no shape, so the type would be a guess and the "+
				"first real value could not correct it -- #42", c.Type)
		}
	}
	if len(schema) != 1 || schema[0].Name != "source_key" {
		t.Errorf("the schema is %v, want just source_key", schema.Names())
	}
}

// The reporter's exact declaration, through the check that refused it.
//
// Not LandingSpread alone: the regression is in what a DRIVER does with the
// row, and the driver is handed `Columns` by `Target.declaredColumns()`. This
// runs the two together, which is the pair the five drivers run.
func TestTheReportersDeclarationAcceptsItsOwnRow(t *testing.T) {
	fields := Schema{
		{Name: "source_key", Type: TypeString},
		{Name: "Area_Drenagem", Type: TypeString},
		{Name: "Codigo_Adicional", Type: TypeString},
	}
	declared := append(
		LandingControlColumns(LandingOptions{UniqueID: true, Keyed: true}),
		fields...)

	land := Landing("ana_station", LandingKey("source_key"), LandingColumns())
	out, err := land(map[string]any{
		"source_key": "k-1", "Area_Drenagem": nil, "Codigo_Adicional": nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := checkRowForTest(declared.Names(), declared, out); err != nil {
		t.Fatalf("the reporter's own fetcher shape is refused:\n\n%v\n\n"+
			"Their vendor fields are often null, and declaring them is the "+
			"documented pattern", err)
	}
}

// The control columns keep their teeth. A chain that does not compose
// `brevis_ingestion_id` is still refused, and that is the half of CheckRow
// that catches a broken Transform rather than an empty value.
func TestAMissingControlColumnIsStillRefused(t *testing.T) {
	declared := LandingControlColumns(LandingOptions{UniqueID: true, Keyed: true})

	land := Landing("ana_station", LandingKey("source_key"), LandingColumns())
	out, err := land(map[string]any{"source_key": "k-1"})
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)
	delete(row, LandingColumnID)

	err = checkRowForTest(declared.Names(), declared, row)
	if err == nil {
		t.Fatal("a row with no brevis_ingestion_id was accepted: that column " +
			"is the identity AND the merge key, and a chain that does not " +
			"write it is broken rather than empty")
	}
	if !strings.Contains(err.Error(), LandingColumnID) {
		t.Errorf("the refusal is %q and does not name the missing column", err)
	}
}
