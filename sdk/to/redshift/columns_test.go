package redshift

import (
	"strings"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// What the COPY names, which is the one part of this path that needs no
// cluster -- and the part the audit after #42's third round found wrong.
func TestColumnsForAsksTheWholeDeclaration(t *testing.T) {
	batch := []core.Envelope{
		{Payload: map[string]any{"id": "A-1", "inactive_at": nil}},
		{Payload: map[string]any{"id": "A-2", "inactive_at": nil}},
	}

	for _, tc := range []struct {
		name string
		opt  core.WriteOptions
		want string
	}{
		{
			// The gateway's shape, and the bug: Columns empty, Schema set.
			// Reading Columns alone fell through to the batch's own keys and
			// named `inactive_at`, which the table does not have.
			name: "a Schema is a declaration",
			opt:  core.WriteOptions{Schema: core.Schema{{Name: "id", Type: core.TypeString}}},
			want: "id",
		},
		{
			// THE CASE THAT DISTINGUISHES THE TWO, and without it a mutation
			// reading opt.Columns survived: with the Schema ignored, the
			// fallback drops `inactive_at` for being nil throughout and gives
			// the same answer by accident. Here the Schema DECLARES it, so the
			// column exists and the COPY has to name it -- otherwise it keeps
			// whatever the table's default puts there instead of the NULL the
			// producer meant.
			name: "a Schema that declares the empty column",
			opt: core.WriteOptions{Schema: core.Schema{
				{Name: "id", Type: core.TypeString},
				{Name: "inactive_at", Type: core.TypeString},
			}},
			want: "id,inactive_at",
		},
		{
			name: "Columns, unchanged",
			opt:  core.WriteOptions{Columns: []string{"id", "inactive_at"}},
			want: "id,inactive_at",
		},
		{
			// Nothing declared: the batch names the columns, less the fields
			// no record gave a value -- naming one asks Redshift for a column
			// that may not exist, over a value never going to be written.
			name: "nothing declared",
			opt:  core.WriteOptions{},
			want: "id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(columnsFor(tc.opt, batch), ",")
			if got != tc.want {
				t.Errorf("the COPY names %q, want %q", got, tc.want)
			}
		})
	}
}

// A field with a VALUE is never dropped, declared or not: that would be the
// silent loss a refusal exists to prevent.
func TestColumnsForNeverDropsAValue(t *testing.T) {
	got := columnsFor(core.WriteOptions{}, []core.Envelope{
		{Payload: map[string]any{"id": "A-1", "kept": nil}},
		{Payload: map[string]any{"id": "A-2", "kept": "x"}},
	})
	if strings.Join(got, ",") != "id,kept" {
		t.Errorf("the COPY names %v: one record gave `kept` a value, so it is "+
			"a real column and leaving it out writes nothing where the "+
			"producer wrote something", got)
	}
}
