package pages

import (
	"context"

	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// Window is the span the Overview reads (screen-overview-v2): its key in the
// URL, how it reads, how long it is, and how its chart is bucketed.
type Window struct {
	Key, Label string
	Span       time.Duration
	Grain      postgres.Grain
	Buckets    int
}

// overviewWindows are the only three, the default first. Twenty-four hours
// cover a full daily cycle -- most schedules are daily, and a shorter window
// would make the success rate swing because of the cut, not because anything
// changed. An hour is a bar over a day, a day is a bar over a week or a month:
// 720 hourly bars would be a texture, not a chart.
var overviewWindows = []Window{
	{Key: "24h", Label: "last 24 hours", Span: 24 * time.Hour, Grain: postgres.ByHour, Buckets: 24},
	{Key: "7d", Label: "last 7 days", Span: 7 * 24 * time.Hour, Grain: postgres.ByDay, Buckets: 7},
	{Key: "30d", Label: "last 30 days", Span: 30 * 24 * time.Hour, Grain: postgres.ByDay, Buckets: 30},
}

// ParseWindow reads ?window=. Anything but the three keys is 24h.
func ParseWindow(key string) Window {
	for _, w := range overviewWindows {
		if w.Key == key {
			return w
		}
	}
	return overviewWindows[0]
}

// chartTitle names the chart by its grain.
func chartTitle(ctx context.Context, w Window) string {
	if w.Grain == postgres.ByDay {
		return i18n.T(ctx, "chart.per_day")
	}
	return i18n.T(ctx, "chart.per_hour")
}
