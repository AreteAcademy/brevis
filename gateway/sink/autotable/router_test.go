package autotable

import (
	"context"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// The wires from the config to the router, which compile whether or not they
// are connected.
//
// Both of these were written, both were right, and a mutation that cut them
// left every test passing: the package tests exercise newMetastore and shape
// directly, and the integration test reads rows out of Postgres, which has no
// table description to check.

// The TTL a file names is the TTL the cache uses.
//
// Cut, it silently falls back to the default -- which is the same number, so
// nothing anywhere would notice until somebody set a different one and it did
// nothing.
func TestTheConfiguredTTLReachesTheCache(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:      gateway.SinkAutoTable,
		Metastore: gateway.MetastoreConfig{Type: gateway.MetastoreMemory, TTL: 5 * time.Minute},
		Into:      &gateway.Sink{Type: "probe"},
	})
	if r.meta.ttl != 5*time.Minute {
		t.Errorf("the cache's TTL is %s, and the file said 5m", r.meta.ttl)
	}
}

// Provider and Entity travel on the envelope, because the BigQuery driver
// writes them into the TABLE's description when it creates one:
//
//	Written by auto_table/app_orders via the Brevis SDK since 2026-09-25.
//
// Nothing here reads them back -- the id is already computed -- so cutting
// them breaks only a string on a table nobody queries in a test. It is also
// the only answer this design has to "what writes here?", six months later.
func TestTheEnvelopeCarriesWhatDescribesTheTable(t *testing.T) {
	r := build(t, gateway.Sink{
		Type: gateway.SinkAutoTable,
		Into: &gateway.Sink{Type: "probe"},
	})

	groups, _, err := r.group([]gateway.Envelope{{Payload: map[string]any{
		"table_name": "app_orders", "data": map[string]any{"id": "A-3"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := groups["app_orders"]
	if len(rows) != 1 {
		t.Fatalf("the batch grouped into %d rows", len(rows))
	}
	if rows[0].Provider != Provider {
		t.Errorf("Provider is %q, want %q", rows[0].Provider, Provider)
	}
	// The TABLE and not a constant: the description names which table it is,
	// and the same string on every table would say nothing.
	if rows[0].Entity != "app_orders" {
		t.Errorf("Entity is %q, want the table name", rows[0].Entity)
	}
}

// build makes a router against a sink that writes nowhere, so these can run
// with no database at all.
func build(t *testing.T, s gateway.Sink) *router {
	t.Helper()
	sinks := gateway.NewSinks()
	sinks.MustRegister(Sink, New)
	sinks.MustRegister("probe", func(gateway.Build) (gateway.Sinker, error) {
		return nowhere{}, nil
	})

	built, err := gateway.BuildSink(gateway.Build{
		Ctx: context.Background(), Sink: s, Sinks: sinks,
		Meta: gateway.NewMemoryMetastore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := built.(*router)
	if !ok {
		t.Fatalf("built a %T", built)
	}
	return r
}

type nowhere struct{}

func (nowhere) Describe() string { return "nowhere" }
func (nowhere) Write(context.Context, []sdk.Envelope) (int64, error) {
	return 0, nil
}

// A field name that cannot be a column has to be refused by Admit, not by
// Write.
//
// This was a real poison batch and it is worth writing down how it looked. Four
// events in one request, one of them carrying `meu-campo`:
//
//	{"accepted":4,"rejected":null}   ← all four told 202
//
// then, two seconds later, the whole batch in the dead letter with a reason
// naming event 2. The three well-formed events belonged to other producers,
// none of whom did anything wrong and none of whom were told. At the batch
// sizes the load test produced -- roughly 8,700 events -- one bad field name
// buries eight thousand.
//
// `open` and `names.check` were already behind Admit. The FIELD names were not,
// because they are the shape's business and the shape only ran at write time.
func TestAdmitRefusesAFieldThatCannotBeAColumn(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: ShapeColumns,
		Into:  &gateway.Sink{Type: "probe"},
	})

	err := r.Admit(map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"id": "A-1", "meu-campo": "x"},
	})
	if err == nil {
		t.Fatal("Admit took a record whose field cannot be a column; at write " +
			"time that fails the whole batch, and every event in it was already " +
			"answered 202")
	}

	// And the same record is fine under `document`, where the record becomes
	// one JSON column and a key is just a key. Refusing it there would be a
	// rule borrowed from the other shape.
	d := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: ShapeDocument,
		Into:  &gateway.Sink{Type: "probe"},
	})
	if err := d.Admit(map[string]any{
		FieldTable: "app_orders",
		FieldData:  map[string]any{"id": "A-1", "meu-campo": "x"},
	}); err != nil {
		t.Errorf("document refused %q, and under that shape it is a key in a "+
			"JSON document and never a column name: %v", "meu-campo", err)
	}
}

// Everything Write can refuse about a record, Admit has to refuse first.
//
// The property, rather than one example: if `group` would fail on a record,
// the producer must learn it in the response instead of the batch learning it
// in the dead letter.
func TestAdmitCatchesWhatWriteWouldHaveRefused(t *testing.T) {
	r := build(t, gateway.Sink{
		Type:  gateway.SinkAutoTable,
		Shape: ShapeColumns,
		Into:  &gateway.Sink{Type: "probe"},
	})

	for _, record := range []map[string]any{
		{"id": "A-1", "meu-campo": "x"}, // a hyphen
		{"id": "A-1", "2fast": "x"},     // starts with a digit
		{"id": "A-1", "com espaço": 1},  // a space
		{"id": "A-1", "": 1},            // empty
	} {
		e := map[string]any{FieldTable: "app_orders", FieldData: record}

		admitted := r.Admit(e) == nil
		_, _, err := r.group([]gateway.Envelope{{Payload: e}})
		written := err == nil

		if admitted != written {
			t.Errorf("record %v: Admit said %v and the write path said %v. A "+
				"record the write path refuses has to be refused per event, or "+
				"it takes the batch with it", record, admitted, written)
		}
	}
}
