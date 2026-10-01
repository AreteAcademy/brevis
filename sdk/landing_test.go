package sdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The landing id is FROZEN, and this is the only thing that says so.
//
// It is a characterisation test on purpose: the values below were read from
// the implementation, and that is the point. Every part of the formula --
// the "auto_table" namespace, the field order, the "|" separator, the
// canonical fingerprint -- is already written into rows that exist. The test
// does not argue that these are the right ids; it argues that they are the
// ids, and that changing any of them is a decision somebody makes on purpose
// rather than a tidy-up nobody notices.
//
// It was written because a mutation survived without it. Renaming
// LandingProvider from "auto_table" to "landing" -- which is exactly the
// "correction" the constant's own comment warns about -- passed the whole
// gateway suite, because the tests there compare the constant against
// itself. The day somebody makes that rename, every id already written
// changes and the next merge duplicates the table.
func TestTheLandingIDIsFrozen(t *testing.T) {
	// Deliberately awkward: a float, an array whose order matters, and a
	// nested object whose keys are out of order, so the canonical form is
	// doing work.
	record := map[string]any{
		"id":     "A-1",
		"total":  15.5,
		"tags":   []any{"x", "y"},
		"nested": map[string]any{"b": 2, "a": 1},
	}

	for _, c := range []struct {
		name, key, want string
	}{
		{"keyed", "A-1", "cf239eb8-b105-5bdd-98a2-7d25cf776170"},
		// No key: the content is the key, in both slots.
		{"keyless", "", "446c3555-9e99-5781-b80c-ac8868cd54b6"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := LandingID("landing.orders", c.key, record)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("LandingID = %s, want %s\n\n"+
					"If this changed on purpose, every landing row already "+
					"written has the old id and the next merge will duplicate "+
					"the table. If it changed by accident -- a renamed "+
					"namespace, a reordered field, a fingerprint that stopped "+
					"being canonical -- this is the test doing its job.",
					got, c.want)
			}
		})
	}
}

// Map iteration is randomised, so a fingerprint that is not canonical gives a
// different id on every call for the same record. That is the one thing it
// exists to prevent.
func TestTheLandingIDDoesNotMoveBetweenCalls(t *testing.T) {
	record := map[string]any{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6}
	first, err := LandingID("t", "k", record)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		got, err := LandingID("t", "k", record)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("two calls, two ids: %s then %s", first, got)
		}
	}
}

// A record that differs anywhere gets a different id, because the last slot
// is the content.
func TestTheLandingIDFollowsTheRecord(t *testing.T) {
	base := map[string]any{"id": "A", "v": 1}
	a, _ := LandingID("t", "A", base)
	b, _ := LandingID("t", "A", map[string]any{"id": "A", "v": 2})
	if a == b {
		t.Error("two different records share an id: the fingerprint is not " +
			"reaching the formula, and a changed record would be absorbed as " +
			"a repeat of the old one")
	}
	// And the array's order is significant: [1,2] is not [2,1].
	x, _ := LandingID("t", "A", map[string]any{"xs": []any{1, 2}})
	y, _ := LandingID("t", "A", map[string]any{"xs": []any{2, 1}})
	if x == y {
		t.Error("[1,2] and [2,1] share an id: the canonical form is sorting " +
			"arrays, and they are different documents")
	}
}

// The layout a pipeline composes into Target.Schema.
//
// Nine columns: the eight the layout owns and the one the producer does. A
// caller who wants their own columns instead of the JSON one takes
// LandingControlColumns and appends theirs.
func TestLandingSchemaIsTheWholeTable(t *testing.T) {
	got := LandingSchema(LandingOptions{})

	want := []string{
		LandingColumnID, LandingColumnRecordKey, LandingColumnOperation,
		LandingColumnReceivedAt, LandingColumnLoadedAt, LandingColumnStream,
		LandingColumnGateway, LandingColumnReceivedBytes, LandingColumnData,
	}
	if len(got) != len(want) {
		t.Fatalf("%d columns, want %d: %v", len(got), len(want), got.Names())
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("column %d is %q, want %q -- the ORDER is the DDL's, and "+
				"a table created here has to match one the gateway created",
				i, got[i].Name, name)
		}
	}

	// The two the gateway fills and a pipeline cannot must be nullable, or a
	// pipeline could not write the row at all.
	for _, name := range []string{
		LandingColumnStream, LandingColumnGateway,
		// The gateway's number counts the WIRE, envelope included. The
		// record's own JSON is about 30% smaller, so filling this would give
		// one column two meanings and make a sum across both paths wrong by
		// whatever share came from which.
		LandingColumnReceivedBytes,
	} {
		for _, c := range got {
			if c.Name == name && c.Required {
				t.Errorf("%s is NOT NULL: a pipeline has no gateway and no "+
					"stream, and a required column it cannot fill is a table "+
					"it cannot write to", name)
			}
		}
	}

	// The control columns are the same eight, without the producer's one.
	control := LandingControlColumns(LandingOptions{})
	if len(control) != len(got)-1 {
		t.Fatalf("%d control columns against %d in the table", len(control), len(got))
	}
	for i := range control {
		if control[i].Name != got[i].Name {
			t.Errorf("control column %d is %q, the table's is %q",
				i, control[i].Name, got[i].Name)
		}
	}
	if control.Has(LandingColumnData) {
		t.Error("the control columns include the producer's column")
	}
}

// The options are what the write mode makes vary, and both are the same
// decision seen twice.
func TestLandingOptionsReachTheColumns(t *testing.T) {
	plain := LandingSchema(LandingOptions{})
	for _, c := range plain {
		if c.Name == LandingColumnID && c.Unique {
			t.Error("the id is UNIQUE by default: BigQuery refuses the " +
				"constraint and `append` does not want it")
		}
		if c.Name == LandingColumnRecordKey && c.Required {
			t.Error("the record key is NOT NULL by default: under `append` a " +
				"record may name none, and the database would refuse the row")
		}
	}

	both := LandingSchema(LandingOptions{UniqueID: true, Keyed: true})
	var sawUnique, sawKeyed bool
	for _, c := range both {
		if c.Name == LandingColumnID && c.Unique {
			sawUnique = true
		}
		if c.Name == LandingColumnRecordKey && c.Required {
			sawKeyed = true
		}
	}
	if !sawUnique {
		t.Error("UniqueID did not reach brevis_ingestion_id")
	}
	if !sawKeyed {
		t.Error("Keyed did not reach brevis_record_key")
	}
}

// Composing is the point: the default is the caller declaring the table, and
// this only saves them typing the part that already exists.
func TestALandingSchemaComposes(t *testing.T) {
	mine := append(LandingSchema(LandingOptions{}),
		Column{Name: "tenant", Type: TypeString, Required: true})

	if !mine.Has("tenant") {
		t.Fatal("an appended column did not survive")
	}
	if !mine.Has(LandingColumnID) || !mine.Has(LandingColumnData) {
		t.Fatal("appending lost the layout")
	}
	// Two callers appending must not write into one array.
	//
	// The first version of this check asked whether a later call could SEE
	// an earlier caller's column, and a shared backing slice passed it: the
	// appended column sits past the returned length, so nobody sees it until
	// somebody else appends over it. The failure is a clobber, not a
	// sighting, and it takes two writers to show.
	a := append(LandingSchema(LandingOptions{}), Column{Name: "tenant_a", Type: TypeString})
	b := append(LandingSchema(LandingOptions{}), Column{Name: "tenant_b", Type: TypeString})
	if got := a[len(a)-1].Name; got != "tenant_a" {
		t.Errorf("after a second caller appended, the first caller's last "+
			"column is %q: the two schemas share a backing array, and one "+
			"pipeline's column lands in another's table", got)
	}
	if got := b[len(b)-1].Name; got != "tenant_b" {
		t.Errorf("the second caller's last column is %q", got)
	}
}

func landingRow(t *testing.T, in map[string]any, table string, opts ...LandingArg) map[string]any {
	t.Helper()
	out, err := Landing(table, opts...)(in)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("Landing returned %T", out)
	}
	return row
}

// The transformer composes exactly the columns LandingSchema declares.
//
// A declared column the chain does not deliver is an error at load naming the
// column; a field the row carries that nothing declared is an error naming
// the field. The two have to agree or neither is worth having.
func TestLandingFillsWhatLandingSchemaDeclares(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A-1", "total": 10},
		"landing.orders", LandingKey("id"))

	// Every declared column, EXCEPT the one the destination fills.
	//
	// brevis_loaded_at is declared so the table gets created with it and a
	// DEFAULT; sending it -- even as NULL -- overrides that default and
	// costs the one measurement the pair exists for. It is the only
	// exception, and naming it here is what keeps it from growing.
	const destinationFills = LandingColumnLoadedAt

	declared := LandingSchema(LandingOptions{})
	if len(row) != len(declared)-1 {
		t.Fatalf("the row has %d columns and the schema declares %d, of which "+
			"exactly one is the destination's:\nrow: %v", len(row), len(declared), row)
	}
	for _, c := range declared {
		_, carried := row[c.Name]
		if c.Name == destinationFills {
			if carried {
				t.Errorf("%q is in the row: it is a database DEFAULT, and "+
					"sending anything overrides it", c.Name)
			}
			continue
		}
		if !carried {
			t.Errorf("the schema declares %q and the row does not carry it", c.Name)
		}
	}
}

// The id comes from the one function that mints it. A second implementation
// here is how two paths stop agreeing.
func TestLandingMintsTheSameIDAsTheFunction(t *testing.T) {
	record := map[string]any{"id": "A-1", "total": 10}
	// The record the fingerprint is over is the PRODUCER's, before any
	// control column is added -- the gateway hashes `data`, not the row.
	want, err := LandingID("landing.orders", "A-1", map[string]any{"id": "A-1", "total": 10})
	if err != nil {
		t.Fatal(err)
	}
	row := landingRow(t, record, "landing.orders", LandingKey("id"))
	if got := row[LandingColumnID]; got != want {
		t.Errorf("the transformer minted %v, LandingID gives %s -- they are "+
			"two implementations of one formula, which is the thing this "+
			"whole layout exists to avoid", got, want)
	}
}

// The gateway's two columns are NULL, and that is the signal.
func TestLandingLeavesTheGatewaysColumnsEmpty(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A"}, "t", LandingKey("id"))
	for _, name := range []string{LandingColumnStream, LandingColumnGateway} {
		v, present := row[name]
		if !present {
			t.Errorf("%s is missing from the row; the schema declares it and "+
				"the load would refuse the batch", name)
		}
		if v != nil {
			t.Errorf("%s is %v, want nil -- a pipeline has no gateway and no "+
				"stream, and a plausible-looking value there is a lie in a "+
				"column somebody aggregates", name, v)
		}
	}
}

// INSERT unless the record says otherwise.
func TestLandingsOperation(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A"}, "t", LandingKey("id"))
	if got := row[LandingColumnOperation]; got != LandingInsert {
		t.Errorf("operation = %v, want %s", got, LandingInsert)
	}

	cdc := landingRow(t, map[string]any{"id": "A", "op": "DELETE"}, "t",
		LandingKey("id"), LandingOperationFrom("op"))
	if got := row[LandingColumnOperation]; got != LandingInsert {
		t.Errorf("the default moved: %v", got)
	}
	if got := cdc[LandingColumnOperation]; got != LandingDelete {
		t.Errorf("operation = %v, want %s -- a source that computes deletes "+
			"has to be able to say so", got, LandingDelete)
	}

	if _, err := Landing("t", LandingOperationFrom("op"))(
		map[string]any{"id": "A", "op": "UPSERT"}); err == nil {
		t.Error("an operation outside the vocabulary was accepted: a landing " +
			"table's history is only readable if the verb set is closed")
	}
}

// With no key the content is the key, exactly as `append` does in the gateway.
func TestLandingWithoutAKey(t *testing.T) {
	record := map[string]any{"a": 1}
	want, _ := LandingID("t", "", map[string]any{"a": 1})
	row := landingRow(t, record, "t")
	if got := row[LandingColumnID]; got != want {
		t.Errorf("keyless id = %v, want %s", got, want)
	}
	if got := row[LandingColumnRecordKey]; got != nil {
		t.Errorf("record_key = %v, want nil: the record named none, and "+
			"inventing one would make it look like the producer's", got)
	}
}

// The prefix is reserved, and the SDK refuses rather than overwrites.
//
// The gateway overwrites, deliberately, because its producer is a stranger
// who must not be able to forge a control field. Here the producer is the
// pipeline's own author: silently replacing what they wrote would hide their
// mistake instead of naming it, which is what IngestionLoadedAt already does.
func TestLandingRefusesAReservedKey(t *testing.T) {
	_, err := Landing("t", LandingKey("id"))(map[string]any{
		"id": "A", LandingColumnReceivedAt: "2020-01-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("a record carrying a brevis_ field was accepted")
	}
	if !strings.Contains(err.Error(), LandingColumnReceivedAt) {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// The record lands whole, and comes back.
func TestLandingKeepsTheRecord(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A", "nested": map[string]any{"x": 1}},
		"t", LandingKey("id"))
	// JSONText and not a plain string: `data` is declared TypeJSON, and a
	// string lands in BigQuery as a JSON value of TYPE string.
	body, marked := row[LandingColumnData].(JSONText)
	if !marked {
		t.Fatalf("the data column is a %T, not a JSONText: on the wire it "+
			"becomes a JSON string and every JSON_VALUE against it is NULL",
			row[LandingColumnData])
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(body), &back); err != nil {
		t.Fatalf("the data column is not JSON: %v", err)
	}
	if back["id"] != "A" {
		t.Errorf("the record did not survive: %v", back)
	}
	if _, leaked := back[LandingColumnID]; leaked {
		t.Error("a control column leaked into the producer's data")
	}
}

// received_at is the SDK's clock, in the format the sibling column uses.
func TestLandingStampsItsOwnClock(t *testing.T) {
	row := landingRow(t, map[string]any{"id": "A"}, "t", LandingKey("id"))
	at, ok := row[LandingColumnReceivedAt].(string)
	if !ok {
		t.Fatalf("received_at is %T", row[LandingColumnReceivedAt])
	}
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("received_at is not RFC 3339: %q", at)
	}
	if time.Since(when) > time.Minute || when.After(time.Now().Add(time.Minute)) {
		t.Errorf("received_at is %s, which is not now", at)
	}
	// loaded_at must be ABSENT, not nil. An explicit NULL overrides the
	// column's DEFAULT -- the first version of this asserted "present and
	// nil", which is exactly the bug, and only the integration test caught
	// it because this one looks at the map instead of the table.
	if v, sent := row[LandingColumnLoadedAt]; sent {
		t.Errorf("loaded_at is in the row as %v: it is a database DEFAULT, "+
			"and sending anything -- NULL included -- overrides it", v)
	}
}

// The spread gives each field its own column, and does not flatten.
func TestLandingSpread(t *testing.T) {
	got, err := LandingSpread(map[string]any{
		"id":       "A-1",
		"qty":      float64(3),
		"customer": map[string]any{"uf": "SP", "id": float64(7)},
		"tags":     []any{"x", "y"},
		"note":     nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got["id"] != "A-1" {
		t.Errorf("id = %v", got["id"])
	}
	// JSON has one number type, so an integer arrives as float64. Rendering
	// it with %v would give 3 here and 2.613e+07 for a large id -- which is
	// the case the renderer exists for.
	if got["qty"] != "3" {
		t.Errorf("qty = %v, want \"3\"", got["qty"])
	}
	// JSONText and not a plain string, and that is the point rather than a
	// detail: the transformer is the last place that can see this value was
	// an object. A destination asked to declare a column for it later cannot,
	// so a pipeline would declare STRING where a gateway declares JSON.
	if got["customer"] != JSONText(`{"id":7,"uf":"SP"}`) {
		t.Errorf("customer = %#v -- a nested object is ONE JSON column under "+
			"the key that held it, MARKED as JSON. It is not flattened into "+
			"customer_id and customer_uf, and neither is it in the gateway",
			got["customer"])
	}
	if got["tags"] != JSONText(`["x","y"]`) {
		t.Errorf("tags = %#v -- an array stays JSON; a record that wants one "+
			"row per element wants ArrayAt", got["tags"])
	}
	// And a producer's ordinary string stays an ordinary string: the marker
	// says "somebody decided this was JSON", never "this looks like JSON".
	looks, err := LandingSpread(map[string]any{"texto": `[1,2]`})
	if err != nil {
		t.Fatal(err)
	}
	if _, marked := looks["texto"].(JSONText); marked {
		t.Error("a producer's literal text `[1,2]` was marked as JSON: the " +
			"marker is a decision carried forward, not a guess about content")
	}
	// NULL and not "": a field sent as null and one sent empty are different
	// facts, and a column cannot tell them apart afterwards.
	if v, present := got["note"]; !present || v != nil {
		t.Errorf("note = %v (present=%v), want a present nil", v, present)
	}
	if len(got) != 5 {
		t.Errorf("%d columns for 5 fields: %v", len(got), got)
	}
}

// A large integer must not become scientific notation.
//
// This is the case the rendering exists for: JSON has one number type, so an
// id arrives as float64 and fmt.Sprint renders 26130000 as 2.613e+07. A
// column carrying that matches nothing.
func TestLandingSpreadDoesNotUseScientificNotation(t *testing.T) {
	got, err := LandingSpread(map[string]any{"external_id": float64(26130000)})
	if err != nil {
		t.Fatal(err)
	}
	if got["external_id"] != "26130000" {
		t.Errorf("external_id = %v, want \"26130000\"", got["external_id"])
	}
}

// A field that cannot be a column name is refused, with the name and the rule.
func TestLandingSpreadRefusesAFieldThatCannotBeAColumn(t *testing.T) {
	for _, name := range []string{"my-field", "2fast", "with space", ""} {
		t.Run(name, func(t *testing.T) {
			_, err := LandingSpread(map[string]any{name: "x"})
			if err == nil {
				t.Fatalf("%q was accepted as a column name", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal does not name the field: %v", err)
			}
		})
	}

	// And the check is callable on its own, because the gateway asks per
	// event before anything is buffered -- one bad name must not fail the
	// batch around it.
	if err := LandingFieldNames(map[string]any{"ok": 1, "not-ok": 2}); err == nil {
		t.Error("LandingFieldNames accepted a name LandingSpread refuses")
	}
	if err := LandingFieldNames(map[string]any{"ok": 1, "_also_ok": 2}); err != nil {
		t.Errorf("a legal name was refused: %v", err)
	}
}

// LandingColumns gives each field a column instead of the record whole.
//
// The record is the consumer's own from issue #40, and it is the one that
// matters: their field is literally called `data`. In the document shape
// `data` is the LAYOUT's column and theirs would be buried inside it; here
// there is no layout column by that name, so the producer's `data` is the
// producer's.
func TestLandingColumnsGivesEachFieldAColumn(t *testing.T) {
	row := landingRow(t, map[string]any{
		"data":       "01/12/2025",
		"series":     "21129",
		"source_key": "21129|01/12/2025",
		"valor":      "8.89",
	}, "bronze.bacen", LandingKey("source_key"), LandingColumns())

	for k, want := range map[string]any{
		"data":       "01/12/2025",
		"series":     "21129",
		"source_key": "21129|01/12/2025",
		"valor":      "8.89",
	} {
		if row[k] != want {
			t.Errorf("%s = %v, want %v", k, row[k], want)
		}
	}
	// The whole record must NOT also be sitting in a JSON column. It would
	// double every byte and give the table two answers to the same question.
	if row["data"] == `{"data":"01/12/2025","series":"21129","source_key":"21129|01/12/2025","valor":"8.89"}` {
		t.Error("the record landed whole in `data`: LandingColumns did not " +
			"reach the row and this is the bug the consumer reported")
	}

	// The control columns are still all there, and still exactly one of them
	// is the destination's.
	for _, c := range LandingControlColumns(LandingOptions{}) {
		if c.Name == LandingColumnLoadedAt {
			if v, sent := row[c.Name]; sent {
				t.Errorf("loaded_at is in the row as %v: it is a DEFAULT", v)
			}
			continue
		}
		if _, ok := row[c.Name]; !ok {
			t.Errorf("the row has no %s: the columns shape drops a control "+
				"column and the load refuses the batch", c.Name)
		}
	}
	if len(row) != len(LandingControlColumns(LandingOptions{}))-1+4 {
		t.Errorf("%d columns: %v", len(row), row)
	}
}

// The id is the SAME in both shapes, and that is the whole point.
//
// It is minted over the PRODUCER's record, before a control column and before
// the spread. Mint it over the spread instead -- where 8.89 is a string and
// an object has become JSON text -- and the same record delivered by a
// pipeline and by a gateway gets two ids, which duplicates the table on the
// next merge.
func TestLandingColumnsDoesNotMoveTheID(t *testing.T) {
	record := map[string]any{
		"source_key": "21129|01/12/2025",
		"valor":      8.89,
		"nested":     map[string]any{"b": 2, "a": 1},
	}
	clone := func() map[string]any {
		out := map[string]any{}
		for k, v := range record {
			out[k] = v
		}
		return out
	}

	document := landingRow(t, clone(), "bronze.bacen", LandingKey("source_key"))
	columns := landingRow(t, clone(), "bronze.bacen", LandingKey("source_key"), LandingColumns())

	if document[LandingColumnID] != columns[LandingColumnID] {
		t.Errorf("the two shapes mint different ids:\n  document %v\n  columns  %v\n\n"+
			"The id is content-addressed over the producer's record, so the "+
			"shape it is STORED in cannot change it. If it can, a table "+
			"switched from one shape to the other duplicates every row.",
			document[LandingColumnID], columns[LandingColumnID])
	}
	// And it is the frozen one, not merely a matching pair.
	want, err := LandingID("bronze.bacen", "21129|01/12/2025", record)
	if err != nil {
		t.Fatal(err)
	}
	if columns[LandingColumnID] != want {
		t.Errorf("id = %v, want %v", columns[LandingColumnID], want)
	}
}

// The values reach the row through the spread, not through some second
// rendering that only this path has.
func TestLandingColumnsRendersLikeTheSpread(t *testing.T) {
	row := landingRow(t, map[string]any{
		"external_id": float64(26130000),
		"customer":    map[string]any{"uf": "SP", "id": float64(7)},
		"tags":        []any{"x", "y"},
		"note":        nil,
	}, "t", LandingColumns())

	if row["external_id"] != "26130000" {
		t.Errorf("external_id = %v, want \"26130000\" -- a float64 rendered "+
			"with %%v is 2.613e+07 and matches nothing", row["external_id"])
	}
	if row["customer"] != JSONText(`{"id":7,"uf":"SP"}`) {
		t.Errorf("customer = %#v -- a nested object is ONE JSON column, not "+
			"flattened, and marked so the destination can declare it JSON",
			row["customer"])
	}
	if row["tags"] != JSONText(`["x","y"]`) {
		t.Errorf("tags = %#v -- an array stays JSON", row["tags"])
	}
	if v, present := row["note"]; !present || v != nil {
		t.Errorf("note = %v (present=%v), want a present nil: null and \"\" "+
			"are different facts", v, present)
	}

	// And NO `data` column.
	//
	// It takes this record to show it. The consumer's own has a field called
	// `data`, so a row that writes the layout's JSON column and then spreads
	// over it looks perfect there -- same count, and the producer's value on
	// top. A mutation doing exactly that survived every other test here. In
	// production it lands a column nothing declared, and the load refuses the
	// batch by name.
	if v, present := row[LandingColumnData]; present {
		t.Errorf("the row carries a `data` column (%v): in this shape the "+
			"record IS the columns, and LandingControlColumns -- what the "+
			"table is composed from here -- declares no such column", v)
	}
	if len(row) != len(LandingControlColumns(LandingOptions{}))-1+4 {
		t.Errorf("%d columns: %v", len(row), row)
	}
}

// A field that cannot be a column name is refused BY NAME, and the record
// does not land half-spread.
func TestLandingColumnsRefusesAFieldThatCannotBeAColumn(t *testing.T) {
	_, err := Landing("t", LandingColumns())(map[string]any{"ok": 1, "my-field": 2})
	if err == nil {
		t.Fatal("\"my-field\" was accepted as a column name")
	}
	if !strings.Contains(err.Error(), "my-field") {
		t.Errorf("the refusal does not name the field: %v", err)
	}

	// The document shape has no such rule: the record goes into one JSON
	// column and a hyphen is a perfectly good JSON key. Refusing it there
	// would break pipelines that have nothing to do with this option.
	if _, err := Landing("t")(map[string]any{"my-field": 2}); err != nil {
		t.Errorf("the document shape refused a JSON key it stores whole: %v", err)
	}
}

// The reserved prefix is still refused, and it has to be refused BEFORE the
// spread -- otherwise a producer's brevis_gateway would be written into the
// row as a column and forge a control field.
func TestLandingColumnsStillRefusesTheReservedPrefix(t *testing.T) {
	_, err := Landing("t", LandingColumns())(map[string]any{
		"id": "A", LandingColumnGateway: "not-a-real-gateway",
	})
	if err == nil {
		t.Fatal("a record carrying brevis_gateway was spread into the row: " +
			"the producer just forged a control column")
	}
	if !strings.Contains(err.Error(), LandingPrefix) {
		t.Errorf("the refusal does not name the prefix: %v", err)
	}
}

// The columns a record declares, and their types.
//
// Six lines of rule and no inference anywhere: the SHAPE decides, never the
// value. That is the whole of what keeps this on the right side of I2 — a
// type read off `21129` would be INT64 today and STRING the day the series
// publishes "21129 (revisado)", and the column would change with nobody
// writing anything.
func TestLandingSchemaOf(t *testing.T) {
	got, err := LandingSchemaOf(map[string]any{
		"valor":    8.89,
		"series":   float64(21129),
		"ok":       true,
		"note":     nil,
		"customer": map[string]any{"uf": "SP"},
		"tags":     []any{"x"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		name string
		typ  ColumnType
	}{
		// Sorted, so the same record always declares the same DDL. Unsorted,
		// two runs over identical data give columns in different orders.
		{"customer", TypeJSON},
		{"note", TypeString},
		{"ok", TypeString},
		{"series", TypeString},
		{"tags", TypeJSON},
		{"valor", TypeString},
	}
	if len(got) != len(want) {
		t.Fatalf("%d columns, want %d: %v", len(got), len(want), got.Names())
	}
	for i, w := range want {
		if got[i].Name != w.name {
			t.Errorf("column %d is %q, want %q -- the order is the DDL's",
				i, got[i].Name, w.name)
		}
		if got[i].Type != w.typ {
			t.Errorf("%s is %s, want %s", got[i].Name, got[i].Type, w.typ)
		}
	}
}

// The type comes from the SHAPE and never from the value.
//
// This is the I2 line, and it is worth a test of its own rather than a
// clause in the one above. A rule that reads the value looks reasonable --
// 21129 IS an integer -- and it is the exact failure the SDK refuses
// everywhere else: "a field that arrived whole today and fractional tomorrow
// changed the column's type with nobody writing anything".
func TestLandingSchemaOfNeverReadsTheValue(t *testing.T) {
	whole, err := LandingSchemaOf(map[string]any{"n": float64(21129)})
	if err != nil {
		t.Fatal(err)
	}
	fractional, err := LandingSchemaOf(map[string]any{"n": 8.89})
	if err != nil {
		t.Fatal(err)
	}
	if whole[0].Type != fractional[0].Type {
		t.Errorf("21129 declares %s and 8.89 declares %s: the type is being "+
			"read off the VALUE. The day this series publishes a whole number "+
			"the column changes, and nobody wrote that",
			whole[0].Type, fractional[0].Type)
	}
	if whole[0].Type != TypeString {
		t.Errorf("a number declares %s, want %s -- everything scalar is text, "+
			"and typing a column is the promotion path: written down and "+
			"reviewed in a diff", whole[0].Type, TypeString)
	}

	// Empty string and nil are both STRING, and true is too. `true` is the
	// one that most invites an exception and does not get one.
	for _, c := range []struct {
		name string
		v    any
	}{{"empty", ""}, {"null", nil}, {"bool", true}, {"int", 42}} {
		s, err := LandingSchemaOf(map[string]any{"f": c.v})
		if err != nil {
			t.Fatal(err)
		}
		if s[0].Type != TypeString {
			t.Errorf("%s declares %s, want %s", c.name, s[0].Type, TypeString)
		}
	}
}

// The schema and the row cannot disagree about what a record contributes.
//
// They are two functions over the same record, and a table declared with one
// set of names and filled with another is a load that fails at the
// destination. Nothing but this test ties them together.
func TestLandingSchemaOfAgreesWithLandingSpread(t *testing.T) {
	record := map[string]any{
		"data": "01/12/2025", "series": float64(21129),
		"source_key": "21129|01/12/2025", "valor": 8.89,
		"meta": map[string]any{"uf": "SP"}, "tags": []any{"a"}, "note": nil,
	}

	schema, err := LandingSchemaOf(record)
	if err != nil {
		t.Fatal(err)
	}
	row, err := LandingSpread(record)
	if err != nil {
		t.Fatal(err)
	}

	if len(schema) != len(row) {
		t.Fatalf("the schema declares %d columns and the row carries %d",
			len(schema), len(row))
	}
	for _, c := range schema {
		if _, present := row[c.Name]; !present {
			t.Errorf("the schema declares %q and the row does not carry it: "+
				"the table would be created with a column nothing fills", c.Name)
		}
	}
	for k := range row {
		if !schema.Has(k) {
			t.Errorf("the row carries %q and the schema does not declare it: "+
				"the load refuses the batch, naming the field", k)
		}
	}
}

// A field that cannot be a column name is refused here too, by name.
func TestLandingSchemaOfRefusesAFieldThatCannotBeAColumn(t *testing.T) {
	_, err := LandingSchemaOf(map[string]any{"ok": 1, "my-field": 2})
	if err == nil {
		t.Fatal("\"my-field\" was accepted as a column name")
	}
	if !strings.Contains(err.Error(), "my-field") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}
