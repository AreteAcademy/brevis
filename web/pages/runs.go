package pages

import (
	"fmt"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
)

// runStates are the Runs list's state choices. The first four are always
// offered; retrying and canceled only when a run is in them, since most
// screens never see either.
var runStates = []struct {
	value, label string
	always       bool
}{
	{"success", "Success", true}, {"failed", "Failure", true},
	{"running", "Running", true}, {"queued", "Queued", true},
	{"retrying", "Retrying", false}, {"canceled", "Canceled", false},
}

// runStateOptions is the state filter, each choice with how many runs it would
// show with the rest of the filter (counts were read without the state).
func runStateOptions(f RunFilter, counts map[string]int) []components.SegOption {
	all := 0
	for _, n := range counts {
		all += n
	}
	out := []components.SegOption{{Label: "Any state", Href: f.With("state", ""),
		Count: fmt.Sprint(all), Current: f.State == ""}}
	for _, s := range runStates {
		if !s.always && counts[s.value] == 0 && f.State != s.value {
			continue
		}
		out = append(out, components.SegOption{Label: s.label, Href: f.With("state", s.value),
			Count: fmt.Sprint(counts[s.value]), Current: f.State == s.value})
	}
	return out
}

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
