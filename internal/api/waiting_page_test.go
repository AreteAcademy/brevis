package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

var holderID = uuid.MustParse("1f3a9c20-0000-4000-8000-000000000001")

func holderAt(id uuid.UUID, workflow string) postgres.Holder {
	return postgres.Holder{RunID: id, Workflow: workflow,
		Since: time.Date(2026, 10, 13, 10, 15, 0, 0, time.UTC)}
}

// queuedPage renders a queued run's page, or its live fragment, with what
// WaitingOn answered.
func queuedPage(t *testing.T, path string, wait *postgres.Wait, waitErr error) (int, string) {
	t.Helper()
	id := uuid.New()
	ui := api.NewUI(nil, nil,
		execsFake{run: dom.Run{ID: id, WorkflowSlug: "daily", Status: dom.StatusQueued},
			wait: wait, waitErr: waitErr},
		nil, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+id.String()+path, nil))
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("the page does not say %q", w)
		}
	}
}

// #95: the daily waits on the group it shares with the */15, and its page says
// which group, what limit, and which run holds the slot since when.
func TestAQueuedRunSaysWhichGroupHoldsIt(t *testing.T) {
	_, body := queuedPage(t, "", &postgres.Wait{Group: "jaffle_models", Limit: 1,
		Holders: []postgres.Holder{holderAt(holderID, "every_15")}}, nil)

	mustContain(t, body,
		"concurrency group", "jaffle_models", "limit 1",
		`href="/runs/`+holderID.String()+`"`, "every_15", "10:15:00 UTC")
}

// No group: the run contends with its own workflow, and the page says that
// rather than inventing a group.
func TestAQueuedRunWithoutAGroupSaysItsWorkflowsLimit(t *testing.T) {
	_, body := queuedPage(t, "", &postgres.Wait{Limit: 1,
		Holders: []postgres.Holder{holderAt(holderID, "daily")}}, nil)

	mustContain(t, body, "this workflow", "concurrency", "limit 1")
	if strings.Contains(body, "concurrency group") {
		t.Error("a run with no group is told it waits on a group")
	}
}

// A free slot with others first in line is a wait too, and a different one.
func TestAQueuedRunBehindOthersSaysHowMany(t *testing.T) {
	_, body := queuedPage(t, "", &postgres.Wait{Group: "warehouse", Limit: 2, Ahead: 2,
		Holders: []postgres.Holder{holderAt(holderID, "loader")}}, nil)

	mustContain(t, body, "2 runs", "ahead of it")
}

// Three holders on the line at most; the rest is a count.
func TestManyHoldersAreThreeLinksAndACount(t *testing.T) {
	var hs []postgres.Holder
	for i := 0; i < 5; i++ {
		hs = append(hs, holderAt(uuid.New(), "loader"))
	}
	_, body := queuedPage(t, "", &postgres.Wait{Group: "warehouse", Limit: 5, Holders: hs}, nil)

	if n := strings.Count(body, `href="/runs/`); n < 3 {
		t.Fatalf("%d run links on the page; the three holders have to be there", n)
	}
	for _, h := range hs[3:] {
		if strings.Contains(body, h.RunID.String()) {
			t.Errorf("holder %s is linked; only three are", h.RunID)
		}
	}
	mustContain(t, body, "and 2 more")
}

// Nothing proved the limit holds it, so nothing is said.
func TestAQueuedRunTheLimitDoesNotHoldSaysNothing(t *testing.T) {
	_, body := queuedPage(t, "", nil, nil)
	if strings.Contains(body, "Waiting for") {
		t.Error("the page gives a reason the query did not find")
	}
}

// The question failing is not the page failing: logs and alerts already work
// this way, and a page missing one line beats a blank one.
func TestAFailedWaitQuestionKeepsThePage(t *testing.T) {
	code, body := queuedPage(t, "", nil, io.ErrUnexpectedEOF)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if strings.Contains(body, "Waiting for") {
		t.Error("a reason is shown although the question failed")
	}
}

// The live fragment carries the line too, so it appears and goes away while
// the page is open, with no polling of its own.
func TestTheLiveFragmentSaysWhatHoldsTheRun(t *testing.T) {
	_, body := queuedPage(t, "/live", &postgres.Wait{Group: "jaffle_models", Limit: 1,
		Holders: []postgres.Holder{holderAt(holderID, "every_15")}}, nil)
	mustContain(t, body, "jaffle_models", "every_15")
}
