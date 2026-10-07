package pages

import (
	"context"
	"strings"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// The description belongs BEHIND the question mark, not in the page.
//
// It shipped as a paragraph above the counts, and a real description is not a
// line: `id_profile_economics` writes fifteen -- why the cube exists, why it
// moved from 05:40 to :40, which upstream refreshes every fifteen minutes, and
// the measured cost of both halves. All of it worth having, and all of it
// pushing the parameters, the rates and the graph below the fold, so the page
// ABOUT a workflow opened on an essay about it.
//
// This pins both halves: the text is reachable, and it is not in the way.
func TestTheDescriptionIsBehindTheQuestionMarkAndNotInTheFlow(t *testing.T) {
	const essay = "Cost, revenue and margin per workspace and profile.\n" +
		"HOURLY, at :40. It used to run once at 05:40.\n" +
		"That stopped matching its own source."

	page := render(t, wf.Workflow{
		Slug: "id_profile_economics", Description: essay,
		Nodes: []wf.Node{{ID: "run"}},
	})

	if !strings.Contains(page, `data-dialogo="about-id_profile_economics"`) {
		t.Error("no question mark beside the name")
	}
	if !strings.Contains(page, `<dialog id="about-id_profile_economics"`) {
		t.Error("the dialog the question mark opens does not exist")
	}

	// The whole text, and its line breaks. A YAML block scalar is written with
	// breaks that mean something -- the reason in one line, the measurement in
	// the next -- and a dialog that collapsed them would be the same wall in a
	// smaller box.
	for _, line := range strings.Split(essay, "\n") {
		if !strings.Contains(page, line) {
			t.Errorf("the dialog is missing a line of the description: %q", line)
		}
	}
	if !strings.Contains(page, "whitespace-pre-wrap") {
		t.Error("the dialog collapses the author's line breaks")
	}

	// And the regression itself: the text exists ONLY inside the dialog.
	//
	// Source order is the wrong thing to assert -- a <dialog> is taken out of
	// the flow when it opens, so where it sits in the markup says nothing. What
	// matters is that no copy of the text is loose in the page, which is what
	// pushed everything below the fold.
	open := strings.Index(page, `<dialog id="about-id_profile_economics"`)
	end := strings.Index(page[open:], "</dialog>")
	if open < 0 || end < 0 {
		t.Fatal("the dialog is not closed")
	}
	dialog := page[open : open+end]
	outside := strings.Replace(page, dialog, "", 1)

	if strings.Contains(outside, "Cost, revenue and margin") {
		t.Error("a copy of the description is loose in the page, outside the dialog")
	}
	if !strings.Contains(dialog, "Cost, revenue and margin") {
		t.Error("the dialog does not hold the description")
	}
}

// A control that opens an empty dialog is a promise the page does not keep.
func TestWithNoDescriptionThereIsNoQuestionMark(t *testing.T) {
	page := render(t, wf.Workflow{Slug: "quiet", Nodes: []wf.Node{{ID: "run"}}})

	if strings.Contains(page, `data-dialogo="about-quiet"`) {
		t.Error("a workflow with no description still offers the question mark")
	}
	if strings.Contains(page, `<dialog id="about-quiet"`) {
		t.Error("an empty dialog was rendered")
	}
}

// The question mark carries an accessible name: it is a lone `?`, so a screen
// reader with no label announces the punctuation and nothing else.
func TestTheQuestionMarkSaysWhatItOpens(t *testing.T) {
	page := render(t, wf.Workflow{
		Slug: "x", Description: "why", Nodes: []wf.Node{{ID: "run"}},
	})
	if !strings.Contains(page, `aria-label="What this workflow is for"`) {
		t.Error("the question mark has no accessible name")
	}
}

func render(t *testing.T, w wf.Workflow) string {
	t.Helper()
	var b strings.Builder
	if err := Workflow(w, []postgres.RunSummary{}, WorkflowStats{}).Render(context.Background(), &b); err != nil {
		t.Fatalf("rendering the workflow page: %v", err)
	}
	return b.String()
}
