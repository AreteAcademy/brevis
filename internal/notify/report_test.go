package notify_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/notify"
)

func sendReport(t *testing.T, r notify.Report) string {
	t.Helper()
	s, body := capture(t, http.StatusOK, "ok")
	if err := s.Report(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return *body
}

func week() notify.Report {
	now := time.Now()
	return notify.Report{
		From: now.Add(-7 * 24 * time.Hour), To: now, Environment: "prod",
		Runs: 140, Succeeded: 133, Failed: 7,
		Workflows: []notify.WorkflowInsight{
			{Slug: "daily_sales", Runs: 100, Failed: 2,
				Min: time.Minute, Avg: 3 * time.Minute, Max: 9 * time.Minute,
				Rows: 4821300, Bytes: 1073741824},
			{Slug: "id_verification", Runs: 40, Failed: 5,
				Min: 30 * time.Second, Avg: 2 * time.Minute, Max: 41 * time.Minute},
		},
	}
}

// The pipelines that failed are what somebody opens the message for, and they
// come first among the sections that have content.
func TestTheReportNamesWhatFailedAndWhatWasSlow(t *testing.T) {
	body := sendReport(t, week())
	for _, want := range []string{
		"140", "133", "7", "95.0%",
		"id_verification", "5 of 40",
		"daily_sales", "2 of 100",
		"41m", // the slowest run, which is the one worth surfacing
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the message does not carry %q", want)
		}
	}
}

// TestTheSlowestIsRankedByMaximumAndNotByAverage.
//
// The maximum is the run that nearly did not finish, and an average hides it
// behind thirty fast ones. id_verification averages less than daily_sales and
// has to come first anyway.
func TestTheSlowestIsRankedByMaximumAndNotByAverage(t *testing.T) {
	r := week()
	slowest := r.Slowest(2)
	if slowest[0].Slug != "id_verification" {
		t.Errorf("slowest[0] = %q; the 41-minute run is the one worth surfacing", slowest[0].Slug)
	}
}

// Rows and bytes appear only when they exist. Most steps are not SDK steps, and
// a "Moved: 0 rows" line every week teaches the reader to skip the section that
// would matter the week it is not zero.
func TestVolumeIsAbsentWhenNothingReportedAny(t *testing.T) {
	r := week()
	for i := range r.Workflows {
		r.Workflows[i].Rows, r.Workflows[i].Bytes = 0, 0
	}
	if body := sendReport(t, r); strings.Contains(body, "Moved") {
		t.Errorf("a volume section was rendered with no volume:\n%s", body)
	}
	// And it IS there when there is something to say.
	if body := sendReport(t, week()); !strings.Contains(body, "Moved") {
		t.Error("the volume section was dropped when there were rows")
	}
}

func TestTheVolumeIsReadable(t *testing.T) {
	body := sendReport(t, week())
	if !strings.Contains(body, "4,821,300 rows") {
		t.Errorf("the row count is not readable:\n%s", body)
	}
	if !strings.Contains(body, "1.0 GB") {
		t.Errorf("the byte count is not readable:\n%s", body)
	}
}

// TestTheMessageSaysWhereTheInfrastructureNumbersAre.
//
// CPU and memory are not in the report and are not zero either: the engine does
// not collect them. Saying where they live is what keeps their absence from
// reading as "nothing to report".
func TestTheMessageSaysWhereTheInfrastructureNumbersAre(t *testing.T) {
	body := sendReport(t, week())
	if !strings.Contains(body, "/metrics") {
		t.Errorf("the message does not say where the infra numbers are:\n%s", body)
	}
	// And it prints no zero for them, which is the failure it exists against.
	for _, absent := range []string{"CPU:", "cpu\\\":0", "Memory:"} {
		if strings.Contains(body, absent) {
			t.Errorf("the message carries %q for a number nothing measures", absent)
		}
	}
}

// An empty window says so. A success rate out of nothing is the most reassuring
// number a report can print and the least true one: an empty week usually means
// the scheduler was down.
func TestAnEmptyWindowSaysSoInsteadOfReportingSuccess(t *testing.T) {
	now := time.Now()
	body := sendReport(t, notify.Report{From: now.Add(-time.Hour), To: now})
	if strings.Contains(body, "100.0%") {
		t.Errorf("an empty window reported a success rate:\n%s", body)
	}
	if !strings.Contains(body, "No run in this window") {
		t.Errorf("an empty window did not say it was empty:\n%s", body)
	}
}

// The environment is in the header for the same reason it is in an alert: a
// staging summary is otherwise indistinguishable from a production one.
func TestAStagingReportSaysSo(t *testing.T) {
	r := week()
	r.Environment = "dev"
	s, body := capture(t, http.StatusOK, "ok")
	s.Environment = "dev"
	if err := s.Report(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*body, "(dev)") {
		t.Errorf("a staging report reads like production:\n%s", *body)
	}
}
