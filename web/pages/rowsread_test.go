package pages

import (
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// ROWS READ COME FROM THE RUN'S STAGES (screen-load-v2 LV2): the landing
// records what was written, and only the stages know what came in -- the
// first map's `in`, or the source's own `rows` when there is no map.
func TestRowsReadComeFromTheStages(t *testing.T) {
	cases := []struct {
		why    string
		stages []postgres.Stage
		want   int64
		none   bool
	}{
		{"the first map's in", []postgres.Stage{
			{Name: "extract", Numbers: map[string]any{"pages": 1.0}},
			{Name: "map", Numbers: map[string]any{"in": 370.0, "out": 370.0}},
			{Name: "map", Numbers: map[string]any{"in": 12.0}},
			{Name: "load", Numbers: map[string]any{"rows": 0.0}},
		}, 370, false},
		{"the source's rows, with no map", []postgres.Stage{
			{Name: "extract", Numbers: map[string]any{"rows": 42.0}},
			{Name: "load", Numbers: map[string]any{"rows": 40.0}},
		}, 42, false},
		{"nothing said", []postgres.Stage{{Name: "check"}, {Name: "load", Numbers: map[string]any{"rows": 3.0}}}, 0, true},
		{"no stages", nil, 0, true},
	}
	for _, c := range cases {
		got := RowsRead(c.stages)
		if c.none {
			if got != nil {
				t.Errorf("%s: got %d, want none", c.why, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("%s: got %v, want %d", c.why, got, c.want)
		}
	}
}
