package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// capped is the wrapper every screen used to get unless it remembered to ask
// for the other one.
const capped = `mx-auto max-w-[1400px] px-6 py-5`

// full is the wrapper every screen gets now.
const full = `<div class="px-6 py-5">`

// EVERY SCREEN USES THE WINDOW, and the flag that decided otherwise is gone.
//
// `Wide bool` was something four screens remembered to set and five forgot --
// Overview, Runs, Projects, Data and Destination were capped at 1400px while
// Workflows, the two DAG screens and /sql used the glass. Nothing said which
// was intended; the default simply won five times.
//
// So the default inverts. A narrow measure is now something a BLOCK asks for
// around its own prose, where the decision is visible beside the text it is
// about, and not something a page forgets at the top.
func TestNoScreenIsCappedAtFourteenHundred(t *testing.T) {
	for _, s := range screens() {
		t.Run(s.name, func(t *testing.T) {
			page := draw(t, s.c)
			if strings.Contains(page, capped) {
				t.Errorf("%s still wraps its content in the 1400px cap", s.name)
			}
		})
	}
}

// AND THE WRAPPER IS WHERE THE HEADER LEAVES OFF.
//
// Position, not presence: this repository has already paid for seven tests
// that asserted `strings.Contains` on a panel rendering OUTSIDE the page and
// passed anyway. A full-bleed <div> somewhere in the document proves nothing
// -- it has to be the one the sticky header hands the screen to.
func TestTheContentWrapperFollowsTheHeader(t *testing.T) {
	for _, s := range screens() {
		t.Run(s.name, func(t *testing.T) {
			page := draw(t, s.c)
			// The PAGE header, not the bar: there are two <header>s in
			// this document and the first one is the navigation.
			head := strings.Index(page, `<header class="sticky`)
			if head < 0 {
				t.Fatalf("%s rendered no page header", s.name)
			}
			at := head + strings.Index(page[head:], "</header>")
			// The next element after the header closes IS the wrapper.
			if rest := page[at+len("</header>"):]; !strings.HasPrefix(strings.TrimSpace(rest), full) {
				t.Errorf("%s does not open its content with %s, it opens with:\n%.120s",
					s.name, full, strings.TrimSpace(rest))
			}
		})
	}
}

type screen struct {
	name string
	c    templ.Component
}

// screens is every signed-in page. `/login` is not one: it does not use Base,
// and a centred card is the right shape for one field and a button.
func screens() []screen {
	return []screen{
		{"overview", Overview(OverviewData{})},
		{"workflows", Workflows(nil, nil, Filter{}, 0, 0)},
		{"runs", Runs(nil, RunFilter{}, 0)},
		{"projects", Projects(nil)},
		{"data", Data(DataView{})},
		{"destination", Target(TargetView{})},
		{"sql", SQL(SQLView{Targets: []string{"postgres://warehouse/db"}})},
		{"workflow", Workflow(wf.Workflow{Slug: "x"}, nil, WorkflowStats{})},
		{"run", Run(run.Run{}, nil, nil)},
	}
}

func draw(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	return b.String()
}
