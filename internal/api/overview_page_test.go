package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// overviewReads answers what the Overview reads, and keeps the window and
// grain it was asked for.
type overviewReads struct {
	readsFake
	ind       postgres.Indicators
	inFlight  []postgres.RunSummary
	recent    []postgres.RunSummary
	schedules []postgres.ScheduleSummary
	asked     *struct {
		span  time.Duration
		grain postgres.Grain
		n     int
	}
}

func (f overviewReads) Indicators(_ context.Context, w time.Duration) (postgres.Indicators, error) {
	f.asked.span = w
	return f.ind, nil
}
func (f overviewReads) RunsPer(_ context.Context, g postgres.Grain, n int) ([]postgres.Bucket, error) {
	f.asked.grain, f.asked.n = g, n
	out := make([]postgres.Bucket, n)
	for i := range out {
		out[i].Start = time.Now().Add(-time.Duration(n-1-i) * 24 * time.Hour)
	}
	return out, nil
}
func (f overviewReads) InFlight(context.Context, int) ([]postgres.RunSummary, error) {
	return f.inFlight, nil
}
func (f overviewReads) LatestRuns(context.Context, int) ([]postgres.RunSummary, error) {
	return f.recent, nil
}
func (f overviewReads) QueueDepth(context.Context) (int, int, error) { return 2, 0, nil }
func (f overviewReads) Schedules(context.Context) ([]postgres.ScheduleSummary, error) {
	return f.schedules, nil
}

func overviewUI(f overviewReads) *api.UI {
	if f.asked == nil {
		f.asked = &struct {
			span  time.Duration
			grain postgres.Grain
			n     int
		}{}
	}
	return api.NewUI(f, nil, nil, nil, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
}

// THE OVERVIEW READS THE WINDOW IN ITS URL (screen-overview-v2 OV1): 24h by
// the hour, 7d and 30d by the day, anything else the default.
func TestTheOverviewReadsItsWindow(t *testing.T) {
	for _, c := range []struct {
		query string
		span  time.Duration
		grain postgres.Grain
		n     int
		label string
	}{
		{"", 24 * time.Hour, postgres.ByHour, 24, "last 24 hours"},
		{"?window=7d", 7 * 24 * time.Hour, postgres.ByDay, 7, "last 7 days"},
		{"?window=30d", 30 * 24 * time.Hour, postgres.ByDay, 30, "last 30 days"},
		{"?window=1y", 24 * time.Hour, postgres.ByHour, 24, "last 24 hours"},
	} {
		f := overviewReads{}
		f.asked = &struct {
			span  time.Duration
			grain postgres.Grain
			n     int
		}{}
		code, body := get(t, overviewUI(f), "/"+c.query)
		if code != http.StatusOK {
			t.Fatalf("%q answered %d", c.query, code)
		}
		if f.asked.span != c.span || f.asked.grain != c.grain || f.asked.n != c.n {
			t.Errorf("%q read %v by %v ×%d", c.query, f.asked.span, f.asked.grain, f.asked.n)
		}
		if !strings.Contains(body, c.label) {
			t.Errorf("%q does not say %q", c.query, c.label)
		}
	}
}
