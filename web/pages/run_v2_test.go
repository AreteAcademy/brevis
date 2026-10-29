package pages

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/run"
)

// THE RUN IS NAMED IN THE TOP BAR (screen-run-v2 RV1): the workflow and the
// run's short id, so two tabs of the same workflow can be told apart, and the
// full id one click from the clipboard.
func TestTheRunsCrumbNamesTheRunAndItsIdCopies(t *testing.T) {
	id := uuid.MustParse("2c7233f8-efc9-4f37-896d-2943d10c0346")
	r := run.Run{ID: id, Status: run.StatusSuccess, WorkflowSlug: "daily_sales", CreatedAt: created}
	page := draw(t, Runs(RunsScreen{Detail: &RunPage{Run: r}}))

	at := strings.Index(page, `aria-label="Breadcrumb"`)
	if at < 0 {
		t.Fatal("no breadcrumb")
	}
	crumb := page[at : at+strings.Index(page[at:], "</nav>")]
	if !strings.Contains(crumb, "daily_sales · 2c7233f8") || !strings.Contains(crumb, "font-mono") {
		t.Errorf("the crumb does not name the run: %s", crumb)
	}
	if !strings.Contains(page, `id="run-id"`) || !strings.Contains(page, `data-copy="#run-id"`) {
		t.Error("the run's id has no copy button")
	}
}
