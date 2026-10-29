package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func slugsOf(rs []postgres.RunSummary) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.WorkflowSlug)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// THE SEARCH IS A PIECE OF THE NAME. "fire" finds every fire workflow; the
// whole slug, which other screens link with, still finds its own -- and a
// `%` or `_` typed in the box is a character, not a wildcard.
func TestTheRunsFilterFindsAPieceOfTheWorkflow(t *testing.T) {
	pool := catalogDB(t)
	for _, slug := range []string{"vendors_fire_inpe", "vendors_fire_goes", "vendorsXfire", "id_analytics"} {
		aged(t, pool, slug, time.Hour, dom.StatusSuccess)
	}
	reads := postgres.NewReadRepo(pool)
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"fire", []string{"vendorsXfire", "vendors_fire_goes", "vendors_fire_inpe"}},
		{"FIRE_", []string{"vendors_fire_goes", "vendors_fire_inpe"}},
		{"s_f", []string{"vendors_fire_goes", "vendors_fire_inpe"}},
		{"id_analytics", []string{"id_analytics"}},
		{"%", nil},
	} {
		got, err := reads.Runs(context.Background(), postgres.RunFilter{Workflow: c.q})
		if err != nil {
			t.Fatal(err)
		}
		if g := slugsOf(got); !slices.Equal(g, c.want) {
			t.Errorf("%q found %v, wanted %v", c.q, g, c.want)
		}
		if n, err := reads.CountRuns(context.Background(), postgres.RunFilter{Workflow: c.q}); err != nil || n != len(got) {
			t.Errorf("%q: CountRuns = %d, %v; the list has %d", c.q, n, err, len(got))
		}
	}
}

// States is several states at once: "in flight" is three of them.
func TestTheRunsFilterTakesSeveralStates(t *testing.T) {
	pool := catalogDB(t)
	for _, s := range []dom.Status{dom.StatusRunning, dom.StatusQueued, dom.StatusFailed, dom.StatusSuccess} {
		aged(t, pool, "w_"+string(s), time.Hour, s)
	}
	got, err := postgres.NewReadRepo(pool).Runs(context.Background(),
		postgres.RunFilter{States: []string{"running", "queued", "retrying"}})
	if err != nil {
		t.Fatal(err)
	}
	if g := slugsOf(got); !slices.Equal(g, []string{"w_queued", "w_running"}) {
		t.Errorf("in flight found %v", g)
	}
}
