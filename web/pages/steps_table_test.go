package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// STEP OUTPUT IS A TABLE WHOSE ROWS OPEN (screen-run-v2 RV4): one 34px row per
// attempt under a 30px header -- step, state, retry, exit, duration, what it
// published -- and every field the cards showed is still there: the error
// opens under its row, with no script, and the log is the Logs tab's
// (screen-runs T5). A mapped step is one row that opens to its instances.
func TestStepOutputIsATableWithNothingLost(t *testing.T) {
	r, logs := reportedRun(t)
	code := 3
	logs = append(logs, postgres.StepLog{NodeID: "discover", MapIndex: run.Unmapped, Attempt: 1, Status: "failed",
		ExitCode: &code, Err: "exited with code 3", Log: "line one\nline two", DurationMs: 1500})
	page := draw(t, Runs(RunsScreen{Detail: &RunPage{Run: r, Logs: logs}}))

	at := strings.Index(page, "data-step-table")
	if at < 0 {
		t.Fatal("the step output is not a table")
	}
	table := page[at:]
	header := table[:strings.Index(table, "</div>")]
	for _, col := range []string{"Step", "State", "Exit", "Duration", "Output"} {
		if !strings.Contains(header, ">"+col+"<") {
			t.Errorf("the header has no %q column", col)
		}
	}
	if !strings.Contains(header, `role="columnheader"`) || !strings.Contains(table, `role="row"`) {
		t.Error("the grid does not say it is a table")
	}
	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(table, " ")), " ")
	for _, want := range []string{"discover", "partitions", "/data/incoming", "↻2", "1.5s",
		"exited with code 3", "load", "2/3 ok", "2026-09-05", "2026-09-07", "no output recorded"} {
		if !strings.Contains(text, want) {
			t.Errorf("the table lost %q", want)
		}
	}
	if !strings.Contains(table, `data-col="exit" class="font-mono text-[12px] text-muted">3</span>`) {
		t.Error("the exit code is not in its column")
	}
	logTab := page[strings.Index(page, `id="logs" data-tab-panel`):strings.Index(page, `id="steps" data-tab-panel`)]
	if !strings.Contains(logTab, "line one") || !strings.Contains(logTab, "line two") {
		t.Error("the step's log is not in the Logs tab")
	}
	if !strings.Contains(table, "<details data-step-group") {
		t.Error("the mapped step is not one row that opens")
	}
	if strings.Contains(table, "rounded-2xl") {
		t.Error("a step attempt is still a card")
	}
}
