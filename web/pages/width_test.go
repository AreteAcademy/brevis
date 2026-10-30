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
// 16px of gutter on a phone, 24px from 960px up, where the sidebar is beside
// the page instead of over it.
const full = `<div class="px-3 pb-10 min-[860px]:px-6">`

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
			// THE WORKBENCH HAS NO PAGE HEADER: it is the one Fill page, and
			// its panes take the height a title would. Its own test holds
			// that (TestTheWorkbenchSpendsNoHeightOnATitle).
			if fillers[s.name] {
				t.Skip("a Fill page draws no page header")
			}
			page := draw(t, s.c)
			// The PAGE header, not the bar: there are two <header>s in
			// this document and the first one is the top bar. Found by its
			// marker rather than a class, which is styling and moves.
			head := strings.Index(page, `<header data-page-header`)
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
		{"workflows", Workflows(WorkflowsScreen{})},
		{"runs", Runs(RunsScreen{})},
		{"projects", Projects(nil)},
		{"loads", Loads(LoadsView{})},
		{"load", Load(LoadView{})},
		{"streams", Streams(StreamsView{})},
		{"stream", StreamPage(StreamView{})},
		{"sql", SQL(SQLView{Targets: []string{"postgres://warehouse/db"}})},
		{"workflow", Workflow(WorkflowPage{Def: wf.Workflow{Slug: "x"}})},
		{"run", Runs(RunsScreen{Detail: &RunPage{Run: run.Run{}}})},
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

// TWO SCREENS FILL THE SHELL: THE WORKBENCH AND RUNS.
//
// `Fill` is a flag, which is the shape W0 deleted -- so it is worth saying
// exactly how it differs. `Wide` could be set by nine screens and five forgot;
// its wrong value was INVISIBLE, because a capped page looks deliberate.
// `Fill` has a named list of callers, and forgetting it gives a page that
// scrolls like every other page -- visible in a second. This refuses the other
// failure: a screen quietly acquiring it.
//
// Runs joined with screen-runs: a list beside a run, each pane scrolling on
// its own, so the list stays put while a long log is read beside it.
var fillers = map[string]bool{"sql": true, "runs": true, "run": true}

func TestOnlyTheNamedScreensFillTheShell(t *testing.T) {
	for _, s := range screens() {
		page := draw(t, s.c)
		fills := strings.Contains(page, "data-fill")
		if fillers[s.name] && !fills {
			t.Errorf("%s does not ask the shell for the whole viewport", s.name)
		}
		if !fillers[s.name] && fills {
			t.Errorf("%s asks to fill the shell", s.name)
		}
	}
}
