package autotable

import (
	"context"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// THE GATEWAY'S OWN UNION. [#43]
//
// `auto_table` with `shape: columns` does not go through the SDK's
// Discovered: it builds a declaration one event at a time and unions them
// here. So the SDK's fold left this untouched, and the published 0.25.0
// image still answered the reporter's flush with
//
//	Field nationalId already exists in schema
//
// Found by running the image against a real BigQuery project, not by reading
// the diff.
func TestTheUnionFoldsWhereTheDestinationDoes(t *testing.T) {
	first := sdk.Schema{
		{Name: "id", Type: sdk.TypeString},
		{Name: "nationalID", Type: sdk.TypeString},
	}
	second := sdk.Schema{
		{Name: "id", Type: sdk.TypeString},
		{Name: "nationalId", Type: sdk.TypeString},
	}

	// Both orders, because the winner is sorted order and not arrival:
	// `nationalID` sorts before `nationalId` ('D' is 0x44, 'd' is 0x64), so
	// in one of these two the winner is not the one that came first.
	for _, c := range []struct {
		name string
		a, b sdk.Schema
	}{
		{"the flush as it arrived", first, second},
		{"the same flush, replayed", second, first},
	} {
		got := merge(merge(nil, c.a, true), c.b, true)
		var names []string
		for _, col := range got {
			names = append(names, col.Name)
		}
		if strings.Join(names, ",") != "id,nationalID" {
			t.Errorf("%s: declared %v, wanted id,nationalID", c.name, names)
		}
	}
}

// And a destination that does NOT fold keeps both, which is correct for
// Postgres and MySQL: a quoted identifier is distinct there.
func TestTheUnionKeepsBothWhereTheDestinationDoesNotFold(t *testing.T) {
	got := merge(
		merge(nil, sdk.Schema{{Name: "nationalID", Type: sdk.TypeString}}, false),
		sdk.Schema{{Name: "nationalId", Type: sdk.TypeString}}, false)
	if len(got) != 2 {
		t.Errorf("declared %v, wanted two columns", got)
	}
}

// ONE EVENT carrying both spellings is refused PER EVENT, at the admitter,
// not by failing the batch around it. That is what `validate` exists for:
// three well-formed events from three producers must not go to the dead
// letter for a fourth producer's mistake.
func TestOneEventWithBothSpellingsIsRefusedAtTheAdmitter(t *testing.T) {
	sh, err := shaperFor(ShapeColumns, true)
	if err != nil {
		t.Fatal(err)
	}
	err = sh.validate(map[string]any{
		"id": "1", "nationalID": "a", "nationalId": "b",
	})
	if err == nil {
		t.Fatal("an event carrying both spellings was admitted; the batch it " +
			"joins fails the CREATE and takes every other producer's events " +
			"to the dead letter with it")
	}
	for _, want := range []string{"nationalID", "nationalId"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// Two EVENTS each carrying one spelling are admitted. That is the reporter's
// actual traffic and it is what this fix exists to land.
func TestTwoEventsEachWithOneSpellingAreAdmitted(t *testing.T) {
	sh, err := shaperFor(ShapeColumns, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []map[string]any{
		{"id": "1", "nationalID": "a"},
		{"id": "2", "nationalId": "b"},
	} {
		if err := sh.validate(r); err != nil {
			t.Errorf("ordinary CDC traffic was refused: %v", err)
		}
	}
}

// And on a destination that does not fold, both are ordinary fields.
func TestBothSpellingsInOneEventAreFineWhereNothingFolds(t *testing.T) {
	sh, err := shaperFor(ShapeColumns, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.validate(map[string]any{"nationalID": "a", "nationalId": "b"}); err != nil {
		t.Errorf("two columns Postgres keeps apart were refused: %v", err)
	}
}

// THE WIRE FROM THE CONFIG TO THE FACT. [#43]
//
// This file's siblings open with "the wires from the config to the router,
// which compile whether or not they are connected", and this is another one.
// `merge` and `shaperFor` take the fold as an argument, so every test above
// passes with `folds` hard-wired either way -- and a gateway whose `into` is
// BigQuery but whose router never asked is exactly what shipped in 0.25.0.
//
// `into.type` is the only thing that may decide it.
func TestTheDestinationDecidesWhetherTheGatewayFolds(t *testing.T) {
	for _, c := range []struct {
		into  string
		folds bool
	}{
		{gateway.SinkBigQuery, true},
		{gateway.SinkPostgres, false},
		{gateway.SinkMySQL, false},
	} {
		r := buildInto(t, c.into)
		if r.folds != c.folds {
			t.Errorf("into.type %q folds=%v, want %v", c.into, r.folds, c.folds)
		}

		// And the shaper got it, which is the half the admitter runs on.
		err := r.shape.validate(map[string]any{"nationalID": "a", "nationalId": "b"})
		if c.folds && err == nil {
			t.Errorf("into.type %q: the admitter accepted both spellings", c.into)
		}
		if !c.folds && err != nil {
			t.Errorf("into.type %q: two columns it keeps apart were refused: %v", c.into, err)
		}
	}
}

// buildInto builds a router over a stand-in destination registered under a
// real sink's NAME, so the wire being tested is the name itself.
func buildInto(t *testing.T, into string) *router {
	t.Helper()

	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	for _, name := range []string{gateway.SinkBigQuery, gateway.SinkPostgres, gateway.SinkMySQL} {
		sinks.MustRegister(name, func(gateway.Build) (gateway.Sinker, error) {
			return nowhere{}, nil
		})
	}

	built, err := gateway.BuildSink(gateway.Build{
		Ctx:  context.Background(),
		Meta: gateway.NewMemoryMetastore(),
		Sink: gateway.Sink{
			Type:  gateway.SinkAutoTable,
			Shape: ShapeColumns,
			Into:  &gateway.Sink{Type: into},
		},
		Sinks: sinks,
	})
	if err != nil {
		t.Fatalf("into.type %q: %v", into, err)
	}
	r, ok := built.(*router)
	if !ok {
		t.Fatalf("built a %T", built)
	}
	return r
}
