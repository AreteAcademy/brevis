package postgres

import (
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
)

// Locate and New must name the same table, or the catalog would list one
// destination and the gateway would write another.
func TestLocateNamesWhatNewWrites(t *testing.T) {
	t.Setenv("PG_DSN", "postgres://u:p@h:5432/analytics")
	cfg := gateway.Sink{Type: Sink, DSNFrom: "PG_DSN", Table: "Landing.Clicks", Write: gateway.WriteAppend}
	built, err := New(gateway.Build{Sink: cfg})
	if err != nil {
		t.Fatal(err)
	}
	located, err := Locate(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := built.(*sink).table.Locate(); located != want {
		t.Fatalf("Locate = %q, New writes %q", located, want)
	}
}
