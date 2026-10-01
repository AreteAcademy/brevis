package load

import (
	"strings"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// The gateway's own config shape, which no SDK test had.
//
// Reported on #42: 4,500 events, 8 batches, every one dead-lettered, and the
// table at 0 rows.
//
//	JSON parsing error in row starting at position 0: No such field: inactive_at.
//
// Every record carried `"inactive_at": null`. The table was created without
// that column, which is right -- a null has no shape. The row carries the key,
// which is right since v0.78.0 -- a DECLARED column that arrived empty has to
// be in the row. BigQuery does not set IgnoreUnknownValues, so it refuses the
// row, and the encoder was supposed to have left the key out.
//
// IT DID NOT, because it was asked the wrong question. The gateway declares
// with a `Schema` and never with `Columns`:
//
//	gateway/sink/bigquery: opt.Schema = b.target.Schema   (and no opt.Columns)
//	sdk/to/bigquery:       Columns: opt.Columns           (empty)
//	sdk/load:              EncodeRows(envelopes, cfg.Columns) -> autodetect
//
// So the encoder read "nothing was declared" on a path that creates the table
// from a declaration. The same mix-up as #42 (3), where ClusterBy was checked
// against the rows.
//
// EVERY SDK TEST DECLARES Columns, which is why the suite was green while this
// path was broken -- for the third release in a row.
func TestTheGatewaysConfigDropsANullNothingDeclares(t *testing.T) {
	// Exactly what sdk/to/bigquery builds for a gateway sink.
	l := &Loader{cfg: &core.LoadConfig{
		Schema: core.Schema{
			{Name: "zarv_metadata_ingestion_id", Type: core.TypeString, Required: true},
			{Name: "id", Type: core.TypeString},
		},
		Evolve: core.EvolveAdditive,
	}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{
			"zarv_metadata_ingestion_id": "i-1", "id": "A-1", "inactive_at": nil,
		}},
		{Payload: map[string]any{
			"zarv_metadata_ingestion_id": "i-2", "id": "A-2", "inactive_at": nil,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "inactive_at") {
		t.Errorf("the wire carries inactive_at and the table has no column "+
			"for it: BigQuery answers `No such field` and the whole batch "+
			"goes to the dead letter.\n\n%s", data)
	}
	if !strings.Contains(string(data), `"id":"A-1"`) {
		t.Errorf("the wire lost id:\n%s", data)
	}
}

// A field null in every record of the batch, against a table that already HAS
// the column, is written -- the declaration is what decides, not the value.
func TestTheGatewaysConfigWritesANullItDeclares(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		Schema: core.Schema{
			{Name: "id", Type: core.TypeString},
			{Name: "inactive_at", Type: core.TypeString},
		},
		Evolve: core.EvolveAdditive,
	}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{"id": "A-1", "inactive_at": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"inactive_at":null`) {
		t.Errorf("the wire dropped a column the table HAS: a null written "+
			"into it is the producer saying the field is empty, and leaving "+
			"it out lets a DEFAULT or a trigger say something else.\n%s", data)
	}
}

// The half that must NOT change: a pipeline that declares Columns and no
// Schema behaves exactly as it did in v0.78.0.
func TestAColumnsOnlyDeclarationIsUnchanged(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		Columns: []string{"id", "declared_empty"},
	}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{
			"id": "A-1", "declared_empty": nil, "undeclared_null": nil,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "undeclared_null") {
		t.Errorf("the wire carries a null nothing declares:\n%s", data)
	}
	if !strings.Contains(string(data), `"declared_empty":null`) {
		t.Errorf("the wire dropped a declared column:\n%s", data)
	}
}

// And autodetect, which is neither: the table is created FROM these rows, so
// a key dropped here is a column that never exists.
func TestAutodetectStillDropsNothing(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{"id": "A-1", "empty": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "empty") {
		t.Errorf("a null was dropped with nothing to judge it by:\n%s", data)
	}
}
