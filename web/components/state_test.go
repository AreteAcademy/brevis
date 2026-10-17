package components

import (
	"context"
	"strings"
	"testing"
)

// TestSkippedIsNotDrawnAsAFailure.
//
// A step that was correctly not run must not read as something that went
// wrong. It is the whole reason `skipped` is a state rather than a flavour of
// failure, and a shared colour would undo that on the one surface anybody
// actually looks at.
func TestSkippedIsNotDrawnAsAFailure(t *testing.T) {
	skipped := stateClass("skipped")
	if skipped == stateClass("failed") {
		t.Error("skipped and failed share a colour")
	}
	if strings.Contains(skipped, "state-failed") {
		t.Errorf("skipped borrows the failure colour: %s", skipped)
	}
}

// And not as `pending` either. The two mean opposite things about the future:
// pending is "has not run YET", skipped is "will not run". Painting them the
// same makes a finished graph look like it is still going.
func TestSkippedIsNotDrawnAsPending(t *testing.T) {
	if stateClass("skipped") == stateClass("pending") {
		t.Error("skipped and pending share a colour")
	}
	if stateDot("skipped") == stateDot("pending") {
		t.Error("skipped and pending share a dot")
	}
}

// An unknown status still falls back to the neutral, and `skipped` must not be
// reaching that fallback -- which is what would happen if the case were
// forgotten, and it would look almost right.
func TestSkippedHasItsOwnColourAndNotTheFallback(t *testing.T) {
	fallback := stateClass("something-nobody-defined")
	if stateClass("skipped") == fallback {
		t.Error("skipped is falling through to the neutral fallback")
	}
	if stateDot("skipped") == stateDot("something-nobody-defined") {
		t.Error("skipped's dot is the fallback")
	}
	if stateLabel("skipped") != "skipped" {
		t.Errorf("label = %q", stateLabel("skipped"))
	}
}

// FRESHNESS IS A DOT AND A WORD too (screen-load-v2 LV1), like a run's state:
// the badge it was is the one density-base left behind. The word and the
// colour are the status' own.
func TestFreshnessIsADotAndAWord(t *testing.T) {
	var b strings.Builder
	if err := Freshness("late").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Contains(got, "rounded-card") || strings.Contains(got, "/13") {
		t.Errorf("freshness is still a badge: %s", got)
	}
	for _, want := range []string{"h-[7px]", "bg-state-queued", "text-state-queued", "late</span>"} {
		if !strings.Contains(got, want) {
			t.Errorf("freshness lost %q: %s", want, got)
		}
	}
}
