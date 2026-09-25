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
