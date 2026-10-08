package load

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Their batch, through the loader's own encode step.
//
// 4,238 rows held a JSON `null` and 145 held SQL `NULL`, for the same
// producer value — decided by whether anything else in the flush window
// happened to carry that key. `WHERE drivers IS NULL` answered 145 of 4,383,
// and every consumer of that table had to write
// `col IS NULL OR JSON_TYPE(col) = 'null'` for ever.
//
// Measured in real BigQuery on 2026-10-08 before this was written:
//
//	id=2  j IS NULL = false   to_json_string = null
func TestANullInADeclaredJSONColumnIsNotWritten(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		Schema: core.Schema{
			{Name: "id", Type: core.TypeString},
			{Name: "j", Type: core.TypeJSON},
		},
	}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{"id": "1", "j": map[string]any{"a": 1}}},
		{Payload: map[string]any{"id": "2", "j": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if !strings.Contains(rows[0], `"a":1`) {
		t.Errorf("the row with a value lost it: %s", rows[0])
	}
	// The key is ABSENT, which is what makes BigQuery write SQL NULL. Sending
	// `"j":null` writes a JSON null, and the two are a different answer to
	// `IS NULL`.
	if strings.Contains(rows[1], `"j"`) {
		t.Errorf("the null is still on the wire, so it lands as a JSON null: %s", rows[1])
	}
}

// 0.80.0 decided that a declared column arriving null is still WRITTEN, and
// that decision is SCOPED by this change rather than reverted: it is right
// for every type whose null is a SQL NULL already. Only JSON has two nulls.
func TestANullInAStringColumnIsStillWritten(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		Schema: core.Schema{
			{Name: "id", Type: core.TypeString},
			{Name: "label", Type: core.TypeString},
		},
	}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{"id": "1", "label": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"label":null`) {
		t.Errorf("a declared STRING column's null was dropped; 0.80.0's decision is reverted, not scoped: %s", data)
	}
}

// The gateway declares with a Schema it DISCOVERED from the batch, never with
// Columns — which is the path that broke three releases running. A JSON
// column that got its type from discovery has to behave like one the
// consumer declared.
func TestItDoesNotMatterWhereTheJSONTypeCameFrom(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  *core.LoadConfig
	}{
		{"the consumer's Schema", &core.LoadConfig{Schema: core.Schema{
			{Name: "id", Type: core.TypeString}, {Name: "j", Type: core.TypeJSON},
		}}},
		{"discovered from the batch", &core.LoadConfig{
			Schema: core.Schema{{Name: "id", Type: core.TypeString}, {Name: "j", Type: core.TypeJSON}},
			Evolve: core.EvolveAdditiveFromPayload,
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := &Loader{cfg: c.cfg}
			data, err := l.encodeRows([]core.Envelope{{Payload: map[string]any{"id": "1", "j": nil}}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"j"`) {
				t.Errorf("the null survived: %s", data)
			}
		})
	}
}

// A key NOTHING declares keeps being dropped, which is 0.79.0's fix and the
// reason `No such field` stopped dead-lettering whole batches.
func TestAnUndeclaredNullIsStillDropped(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		Schema: core.Schema{{Name: "id", Type: core.TypeString}},
	}}
	data, err := l.encodeRows([]core.Envelope{{Payload: map[string]any{"id": "1", "stray": nil}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "stray") {
		t.Errorf("an undeclared key reached the wire: %s", data)
	}
}

// WHAT MUST NOT CHANGE, and the reason it cannot.
//
// The fix is BigQuery's alone. Postgres and MySQL never used EncodeRows --
// their drivers bind values, and a Go nil binds to SQL NULL before any JSON
// encoding happens. Redshift has its own encoder, EncodeNDJSON, and does not
// come through here either.
//
// Pinned as a test rather than written in a comment, because "only BigQuery
// is affected" is the kind of claim that is true when it is made and false
// after a refactor nobody connected to it.
func TestOnlyBigQueryGoesThroughThisEncoder(t *testing.T) {
	// If another destination starts calling EncodeRows, this test is where
	// somebody is told to think about the rule above before it ships.
	for _, path := range []string{
		"../to/postgres/postgres.go",
		"../to/mysql/mysql.go",
		"../to/redshift/redshift.go",
	} {
		src, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "EncodeRows(") {
			t.Errorf("%s now encodes through EncodeRows. A null in a JSON column is "+
				"DROPPED there, which is right for BigQuery and has to be decided "+
				"again for this destination: does its JSON null answer IS NULL?", path)
		}
	}
}
