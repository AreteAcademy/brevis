package pages

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func failedRunPage() RunPage {
	id := uuid.MustParse("a91f03c2-0000-4000-8000-000000000000")
	logs := []postgres.StepLog{
		{NodeID: "build", MapIndex: run.Unmapped, Status: "success", Log: "building\n"},
		{NodeID: "test", MapIndex: run.Unmapped, Attempt: 1, Status: "failed", ExitCode: exit(1),
			Log: "[WARNING]: severity warn\nError: 1 test failed\n"},
		{NodeID: "notify", MapIndex: run.Unmapped, Status: "running"},
	}
	return RunPage{
		Run:  run.Run{ID: id, WorkflowSlug: "environment_alert", Status: run.StatusFailed, CreatedAt: created},
		Logs: logs,
		Cause: &diagnosis.Cause{Step: "test", MapIndex: run.Unmapped, Attempt: 1, LineNo: 2,
			Line: "Error: 1 test failed"},
	}
}

// THE BODY IS TABS: Logs first, with how many lines; Steps; Parameters --
// tabs by the hash, so a link can open one, and stacked with no script.
func TestTheRunsBodyIsTabs(t *testing.T) {
	page := draw(t, RunPane(failedRunPage()))
	for _, want := range []string{
		`id="run-tabs" data-hash-tabs`, `data-hash-tabs-bar data-js`,
		`data-tab="logs"`, `data-tab="steps"`, `data-tab="params"`,
		`data-tab-panel="logs"`, `data-tab-panel="steps"`, `data-tab-panel="params"`,
		`#run-tabs [data-tab-panel]{display:none}`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s", want)
		}
	}
	if !strings.Contains(between(page, `data-tab="logs"`, "</button>"), ">3<") {
		t.Error("the Logs tab does not count its lines")
	}
	if strings.Index(page, `id="logs" data-tab-panel`) > strings.Index(page, `id="steps" data-tab-panel`) {
		t.Error("Logs is not the first panel")
	}
}

func between(s, from, to string) string {
	at := strings.Index(s, from)
	if at < 0 {
		return ""
	}
	end := strings.Index(s[at:], to)
	if end < 0 {
		return s[at:]
	}
	return s[at : at+end]
}

// THE LOGS TAB: each attempt under its heading, each line numbered with its
// level, the cause marked where the head's button will find it, a running
// step saying its log is coming, and the whole log to download.
func TestTheLogsTabDrawsTheRunsLog(t *testing.T) {
	p := failedRunPage()
	panel := between(draw(t, RunPane(p)), `id="logs" data-tab-panel="logs"`, `id="steps" data-tab-panel="steps"`)
	for _, want := range []string{
		"test · attempt 2", `data-n="2" data-level="warn"`, `data-n="3" data-level="err"`,
		`id="log-cause"`, "notify is running — its log arrives when it ends",
		`href="/runs/a91f03c2-0000-4000-8000-000000000000/log.txt"`,
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("the Logs tab lacks %s", want)
		}
	}
	if strings.Count(panel, `id="log-cause"`) != 1 {
		t.Error("the cause is not marked once")
	}
}

// A ten-megabyte log does not become a ten-megabyte page.
func TestAHugeLogIsABoundedPage(t *testing.T) {
	big := strings.Repeat("fetched a page of rows from the upstream api\n", 10<<20/45)
	p := failedRunPage()
	p.Logs = []postgres.StepLog{{NodeID: "load", MapIndex: run.Unmapped, Status: "failed", Log: big}}
	p.Cause = nil
	page := draw(t, RunPane(p))
	if len(page) > 1<<20 {
		t.Errorf("a %d-byte log drew a %d-byte page", len(big), len(page))
	}
	if !strings.Contains(page, "earlier lines — in the download") {
		t.Error("the lines left out are not counted")
	}
}
