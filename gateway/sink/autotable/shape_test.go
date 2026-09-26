package autotable

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// The envelope, taken apart — and what it refuses.
//
// Every refusal here reaches the producer in the response, at the moment they
// can still fix it, and every well-formed event in the same request lands.
func TestTheEnvelopeRefusesWhatItCannotHonour(t *testing.T) {
	for _, c := range []struct {
		name string
		in   map[string]any
		says string
	}{
		{"no table", map[string]any{"data": map[string]any{"id": "A"}}, "table_name"},
		{"no data", map[string]any{"table_name": "app_orders"}, `no "data"`},
		{"data is not an object", map[string]any{"table_name": "t", "data": "x"}, "not a JSON object"},
		{"data is empty", map[string]any{"table_name": "t", "data": map[string]any{}}, "is empty"},
		{
			// Without this a producer forges a control field, and a forged
			// brevis_received_at is worse than none because it looks real.
			name: "a record using the reserved prefix",
			in: map[string]any{"table_name": "t", "data": map[string]any{
				"id": "A", "brevis_received_at": "2020-01-01T00:00:00Z"}},
			says: "reserved",
		},
		{
			// NAMED and absent, which is a mistake in either write mode: the
			// producer said `order_id` and there is no `order_id`, so a row
			// landed here would carry a record key they did not ask for.
			//
			// The default `id` simply not being there is the OTHER case, and
			// `open` does not answer it -- the write mode does. See
			// TestTheKeyIsRequiredOnlyByMerge.
			name: "a unique key the record does not carry",
			in: map[string]any{"table_name": "t", "unique_key": "order_id",
				"data": map[string]any{"id": "A"}},
			says: `"order_id"`,
		},
		{
			name: "an operation that is not one",
			in: map[string]any{"table_name": "t", "operation": "MERGE",
				"data": map[string]any{"id": "A"}},
			says: "use INSERT, UPDATE or DELETE",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := open(c.in)
			if err == nil {
				t.Fatal("it was accepted")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say %q: %v", c.says, err)
			}
		})
	}
}

// The defaults: `id` for the key, INSERT for the operation.
func TestTheEnvelopeDefaults(t *testing.T) {
	env, err := open(map[string]any{
		"table_name": "app_orders",
		"data":       map[string]any{"id": "A-3", "total": 150},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env.key != "A-3" {
		t.Errorf("the key is %q, and `id` is the default unique_key", env.key)
	}
	if env.op != OpInsert {
		t.Errorf("the operation is %q, want %s", env.op, OpInsert)
	}
	// And a named key wins.
	env, err = open(map[string]any{
		"table_name": "app_orders", "unique_key": "order_id",
		"data": map[string]any{"id": "ignored", "order_id": "A-9"},
	})
	if err != nil || env.key != "A-9" {
		t.Errorf("unique_key did not name the field: %q %v", env.key, err)
	}
}

// The identity has NO clock in it, and that is the whole point.
//
// `occurred_at` used to be the client's and part of the formula. Making arrival
// our responsibility would have put time.Now() here — different on every
// delivery — so every retry would have been a new event and `merge` would have
// stopped absorbing anything.
func TestTheIdentityHasNoClockAndAbsorbsARetry(t *testing.T) {
	body := func() map[string]any {
		return map[string]any{
			"table_name": "app_orders",
			"data": map[string]any{
				"id": "A-3", "total": 150,
				"customer": map[string]any{"id": float64(7), "uf": "SP"},
			},
		}
	}
	first, err := mustOpen(t, body()).identify()
	if err != nil {
		t.Fatal(err)
	}

	// The same body again, at a different moment: the same id.
	again, _ := mustOpen(t, body()).identify()
	if first != again {
		t.Errorf("the same record produced two ids:\n  %s\n  %s", first, again)
	}
	if len(first) != 36 {
		t.Errorf("the id is %q", first)
	}

	// The same record with CHANGED content: a different id, so both versions
	// land. That is what makes an UPDATE a second row rather than a no-op.
	changed := body()
	changed["data"].(map[string]any)["total"] = 999
	updated, _ := mustOpen(t, changed).identify()
	if updated == first {
		t.Error("a changed record kept the id, so an UPDATE would be swallowed")
	}

	// And the same content in another TABLE is another row.
	other := body()
	other["table_name"] = "app_events"
	elsewhere, _ := mustOpen(t, other).identify()
	if elsewhere == first {
		t.Error("the same content in two tables produced one id")
	}
}

// The columns the gateway owns, and the one it deliberately leaves out.
func TestTheRowCarriesTheFixedColumns(t *testing.T) {
	now := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	env := mustOpen(t, map[string]any{
		"table_name": "app_orders", "operation": "update",
		"data": map[string]any{"id": "A-3"},
	})
	row, err := env.row(now, "tables", "ingestao")
	if err != nil {
		t.Fatal(err)
	}

	for k, want := range map[string]any{
		ColumnRecordKey:  "A-3",
		ColumnOperation:  OpUpdate, // lowercase in, upper out
		ColumnReceivedAt: "2026-09-25T14:00:00Z",
		ColumnStream:     "tables",
		ColumnGateway:    "ingestao",
	} {
		if row[k] != want {
			t.Errorf("%s is %v, want %v", k, row[k], want)
		}
	}
	if id, _ := row[ColumnID].(string); len(id) != 36 {
		t.Errorf("%s is %q", ColumnID, row[ColumnID])
	}

	// brevis_loaded_at is ABSENT on purpose: the destination's DEFAULT stamps
	// it, because the gateway knows the dispatch time and only the destination
	// knows the write time. Sending a value would make the two columns say the
	// same thing and the latency between them zero.
	if _, present := row[ColumnLoadedAt]; present {
		t.Errorf("%s was sent, and it must be the destination's DEFAULT", ColumnLoadedAt)
	}
}

// Scalar → STRING, object and array → JSON. No inference, ever.
func TestTheColumnsShapeTypesNothing(t *testing.T) {
	record := map[string]any{
		"id":       "A-3",
		"total":    float64(150),
		"paid":     true,
		"cancel":   nil,
		"customer": map[string]any{"id": float64(7)},
		"items":    []any{map[string]any{"sku": "X"}},
	}
	got, err := columns{}.schema(record)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]sdk.ColumnType{
		"id": sdk.TypeString, "total": sdk.TypeString, "paid": sdk.TypeString,
		"cancel": sdk.TypeString, "customer": sdk.TypeJSON, "items": sdk.TypeJSON,
	}
	if len(got) != len(want) {
		t.Fatalf("declared %d columns, want %d", len(got), len(want))
	}
	for _, c := range got {
		if want[c.Name] != c.Type {
			t.Errorf("%s is %s, want %s", c.Name, c.Type, want[c.Name])
		}
	}

	// And the declaration is SORTED, so identical records never produce two
	// different CREATE TABLEs.
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Errorf("the declaration is not sorted: %v after %v", got[i].Name, got[i-1].Name)
		}
	}
}

// A field name that Postgres would accept quoted and BigQuery would not.
//
// That is the trap: the table is created on Postgres, the same producer works
// for months, and it breaks the day somebody points a stream at BigQuery. So
// the narrowest of the four rules is the one applied everywhere.
func TestAFieldNameBigQueryRefusesIsRefusedEverywhere(t *testing.T) {
	for _, bad := range []string{"novo-campo", "cliente.uf", "1abc", "com espaço", `as"pas`, ""} {
		if _, err := (columns{}).schema(map[string]any{bad: "x"}); err == nil {
			t.Errorf("the field name %q was accepted", bad)
		}
	}
	for _, good := range []string{"id", "_private", "novo_campo", "Total2"} {
		if _, err := (columns{}).schema(map[string]any{good: "x"}); err != nil {
			t.Errorf("the field name %q was refused: %v", good, err)
		}
	}
}

// The document shape declares one column whatever the record holds, which is
// what makes a table created there a template rather than a decision.
func TestTheDocumentShapeIsAlwaysOneColumn(t *testing.T) {
	for _, record := range []map[string]any{
		{"id": "A"},
		{"id": "A", "novo_campo": "x", "outro": map[string]any{"a": 1}},
	} {
		got, err := document{}.schema(record)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != ColumnData || got[0].Type != sdk.TypeJSON {
			t.Errorf("the document shape declared %v", got)
		}
	}
}

func mustOpen(t *testing.T, in map[string]any) envelope {
	t.Helper()
	env, err := open(in)
	if err != nil {
		t.Fatal(err)
	}
	return env
}
