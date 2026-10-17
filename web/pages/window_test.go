package pages

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// THE OVERVIEW'S WINDOW COMES FROM THE URL, AND ONLY THREE ARE ACCEPTED
// (screen-overview-v2 OV1): 24 hours by the hour, 7 and 30 days by the day.
// Anything else is the default -- a link with a typo still opens a page.
func TestTheOverviewsWindowIsOneOfThree(t *testing.T) {
	cases := map[string]Window{
		"24h": {Key: "24h", Label: "last 24 hours", Span: 24 * time.Hour, Grain: postgres.ByHour, Buckets: 24},
		"7d":  {Key: "7d", Label: "last 7 days", Span: 7 * 24 * time.Hour, Grain: postgres.ByDay, Buckets: 7},
		"30d": {Key: "30d", Label: "last 30 days", Span: 30 * 24 * time.Hour, Grain: postgres.ByDay, Buckets: 30},
	}
	for in, want := range cases {
		if got := ParseWindow(in); got != want {
			t.Errorf("%q = %+v, want %+v", in, got, want)
		}
	}
	for _, in := range []string{"", "1y", "7D", "drop table runs"} {
		if got := ParseWindow(in); got.Key != "24h" {
			t.Errorf("%q fell to %q, not the default", in, got.Key)
		}
	}
}
