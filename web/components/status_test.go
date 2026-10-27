package components

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// statusKinds is every state the console draws: a run's or a step's, and a
// destination's against its schedule. One list, because the point is that a
// reader learns ONE vocabulary.
var statusKinds = StatusKinds()

// AND THE LIST AND THE TABLE ARE THE SAME SET: a kind in the table that the
// gallery never shows is a kind nobody looked at.
func TestEveryDeclaredStateIsListed(t *testing.T) {
	if len(statusKinds) != len(statuses) {
		t.Errorf("%d kinds listed, %d declared", len(statusKinds), len(statuses))
	}
}

// synonyms share a shape and a colour on purpose: the same meaning, said by
// two parts of the console.
var synonyms = map[[2]string]bool{
	{"success", "on_time"}: true,
	{"canceled", "paused"}: true,
}

// A STATE IS A SHAPE, A WORD AND A COLOUR, and never the colour alone. Two
// states that mean different things in the same colour -- failed and stale,
// late and queued -- are told apart by their shape, which is what somebody
// who cannot tell the colours apart has left.
func TestNoTwoStatesShareAShapeAndAColour(t *testing.T) {
	type look struct{ shape, colour string }
	seen := map[look]string{}
	for _, k := range statusKinds {
		s, ok := statuses[k]
		if !ok {
			t.Errorf("%s has no entry", k)
			continue
		}
		if s.label == "" {
			t.Errorf("%s has no word", k)
		}
		l := look{s.shape, s.colour}
		if other, dup := seen[l]; dup && !synonyms[[2]string{other, k}] && !synonyms[[2]string{k, other}] {
			t.Errorf("%s and %s are both a %s in %s, and mean different things", other, k, s.shape, s.colour)
		}
		seen[l] = k
	}
}

func TestEveryStateIsDrawnWithItsShapeAndItsWord(t *testing.T) {
	for _, k := range statusKinds {
		html := draw(t, Status(k))
		s := statuses[k]
		for _, want := range []string{"st-" + s.shape, s.colour, ">" + s.label + "<"} {
			if !strings.Contains(html, want) {
				t.Errorf("%s: %q is missing from %s", k, want, html)
			}
		}
	}
}

// THE OLD NAMES KEEP WORKING. State, Dot and Freshness are called from every
// screen; they draw through Status now, with the words they always had.
func TestTheOldComponentsDrawThroughStatus(t *testing.T) {
	for _, c := range []struct {
		html, shape, word string
	}{
		{draw(t, State("failed")), "st-diamond", ">failed<"},
		{draw(t, State("")), "st-ring-dashed", ">never ran<"},
		{draw(t, Freshness("stale")), "st-diamond-hollow", ">stale<"},
		{draw(t, Freshness("whatever")), "st-ring", ">no schedule<"},
		{draw(t, Dot("running")), "st-spin", `title="running"`},
	} {
		// The shape class ends at a space or the attribute's quote, so
		// st-ring does not pass on st-ring-dashed.
		if !strings.Contains(c.html, c.shape+" ") && !strings.Contains(c.html, c.shape+`"`) {
			t.Errorf("want %s in %s", c.shape, c.html)
		}
		if !strings.Contains(c.html, c.word) {
			t.Errorf("want %s in %s", c.word, c.html)
		}
	}
}

func draw(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
