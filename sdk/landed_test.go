package sdk

import (
	"context"
	"testing"
)

// locatingTarget is keepingTarget that can name its destination.
type locatingTarget struct {
	keepingTarget
	target string
}

func (l locatingTarget) Locate() string { return l.target }

func (l locatingTarget) Write(ctx context.Context, envs []Envelope, opt WriteOptions) (*LoadResult, error) {
	return l.keepingTarget.Write(ctx, envs, opt)
}

func landingsIn(eventos []map[string]any) []map[string]any {
	var out []map[string]any
	for _, ev := range eventos {
		if ev["type"] == "landed" {
			out = append(out, ev)
		}
	}
	return out
}

func twoLandable() countedSource {
	return countedSource{
		records:  []any{map[string]any{"id": 1}, map[string]any{"id": 2}},
		leituras: new(int),
	}
}

func TestASuccessfulLoadLandsOnceAfterTheLoadCloses(t *testing.T) {
	var box []Envelope
	p := pipelineDeTeste(twoLandable(), &box)
	p.Target.To = locatingTarget{keepingTarget{received: &box}, "postgres://analytics/public/orders"}

	eventos, err := stagesOf(t, p)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	got := landingsIn(eventos)
	if len(got) != 1 {
		t.Fatalf("landings = %d, want exactly 1: %v", len(got), eventos)
	}
	l := got[0]
	if l["target"] != "postgres://analytics/public/orders" {
		t.Errorf("target = %v", l["target"])
	}
	if l["rows"] != float64(2) {
		t.Errorf("rows = %v, want 2", l["rows"])
	}
	if _, has := l["bytes"]; has {
		t.Errorf("bytes = %v: a driver that did not count bytes must not claim zero", l["bytes"])
	}

	// After the target's box closes: the engine reads the landing as a fact
	// about a finished load, and a landing announced while the load still ran
	// would be a promise.
	closed, landed := -1, -1
	for i, ev := range eventos {
		if ev["type"] == "stage" && ev["name"] == PhaseTarget && ev["state"] == StateDone {
			closed = i
		}
		if ev["type"] == "landed" {
			landed = i
		}
	}
	if closed < 0 || landed < closed {
		t.Errorf("landed at %d, load closed at %d: the landing must come after", landed, closed)
	}
}

// Zero rows is information. A source with nothing new today still delivered,
// and the writer is on time; leaving `rows` out would read as "did not say".
func TestALoadOfNothingStillLandsAndSaysZero(t *testing.T) {
	var box []Envelope
	p := pipelineDeTeste(countedSource{leituras: new(int)}, &box)
	p.Target.To = locatingTarget{keepingTarget{received: &box}, "postgres://analytics/public/orders"}

	eventos, err := stagesOf(t, p)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	got := landingsIn(eventos)
	if len(got) != 1 || got[0]["rows"] != float64(0) {
		t.Fatalf("landings = %v, want one with rows 0", got)
	}
}

func TestAFailedLoadLandsNothing(t *testing.T) {
	var box []Envelope
	p := pipelineDeTeste(twoLandable(), &box)
	p.Target.To = locatingTarget{keepingTarget{received: &box, fail: true}, "postgres://analytics/public/orders"}

	eventos, _ := stagesOf(t, p)
	if got := landingsIn(eventos); len(got) != 0 {
		t.Fatalf("a refused load landed: %v", got)
	}
}

// A writer somebody wrote for their own destination keeps working, and lands
// nothing: Locator is optional.
func TestAWriterThatCannotNameItsDestinationLandsNothing(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, pipelineDeTeste(twoLandable(), &box))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if got := landingsIn(eventos); len(got) != 0 {
		t.Fatalf("landings = %v, want none", got)
	}
}

// An empty answer from Locate() is "cannot say", not a target.
func TestAnEmptyLocateLandsNothing(t *testing.T) {
	var box []Envelope
	p := pipelineDeTeste(twoLandable(), &box)
	p.Target.To = locatingTarget{keepingTarget{received: &box}, ""}

	eventos, _ := stagesOf(t, p)
	if got := landingsIn(eventos); len(got) != 0 {
		t.Fatalf("landings = %v, want none", got)
	}
}
