package pages

import (
	"fmt"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// longestDuration is the page's longest run, the scale of its duration bars.
func longestDuration(runs []postgres.RunSummary) time.Duration {
	var max time.Duration
	for _, r := range runs {
		if r.Duration != nil && *r.Duration > max {
			max = *r.Duration
		}
	}
	return max
}

// durationShare is a run's bar width against the longest, as CSS; "" for a
// run with no duration yet, which gets no bar rather than an empty one.
func durationShare(d *time.Duration, longest time.Duration) string {
	if d == nil || longest <= 0 {
		return ""
	}
	pct := int(float64(*d) / float64(longest) * 100)
	if pct < 1 {
		pct = 1
	}
	return fmt.Sprintf("%d%%", pct)
}

// runCrumb names the run in the top bar: its workflow and the first block of
// its id, which is what tells two tabs of the same workflow apart.
func runCrumb(r run.Run) string {
	return r.WorkflowSlug + " · " + r.ID.String()[:8]
}
