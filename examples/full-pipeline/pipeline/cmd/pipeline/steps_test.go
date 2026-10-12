package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// writeReport puts a report for date in dir, written `age` ago.
func writeReport(t *testing.T, dir, date string, age time.Duration) {
	t.Helper()
	day := filepath.Join(dir, date)
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(day, "part.ndjson")
	if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(f, at, at); err != nil {
		t.Fatal(err)
	}
}

// useReports points the report directory at a fresh one, for a run whose
// slot is 2026-10-12: the day `report` names its directory after.
func useReports(t *testing.T) string {
	t.Helper()
	t.Setenv("BREVIS_AUTO_PARAMS", `{"adjusted_at": "2026-10-12T06:00:00Z", "date": "2026-10-12"}`)
	dir := t.TempDir()
	old := reports
	reports = dir
	t.Cleanup(func() { reports = old })
	return dir
}

// A data directory a month old still holds the reports of every earlier run.
// A fresh report beside them is a fresh report.
func TestAnOldReportBesideAFreshOneIsNotStale(t *testing.T) {
	dir := useReports(t)
	writeReport(t, dir, "2026-09-09", 30*24*time.Hour)
	writeReport(t, dir, "2026-10-12", time.Minute)
	if err := qualityFreshness(); err != nil {
		t.Fatalf("refused: %v", err)
	}
}

// And when nothing was written for a day, the check still says so.
func TestNoReportFromTheLastDayIsStale(t *testing.T) {
	dir := useReports(t)
	writeReport(t, dir, "2026-10-10", 50*time.Hour)
	writeReport(t, dir, "2026-10-11", 30*time.Hour)
	if err := qualityFreshness(); err == nil {
		t.Fatal("accepted a newest report thirty hours old")
	}
}

// writeRows puts a report for date in dir with n rows, written `age` ago.
func writeRows(t *testing.T, dir, date string, n int, age time.Duration) {
	t.Helper()
	day := filepath.Join(dir, date)
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(day, "part-"+strconv.Itoa(n)+".ndjson")
	if err := os.WriteFile(f, []byte(strings.Repeat("{}\n", n)), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(f, at, at); err != nil {
		t.Fatal(err)
	}
}

// stdout runs f and returns what it printed.
func stdout(t *testing.T, f func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := f()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("failed: %v", runErr)
	}
	return string(out)
}

// The count is the report's, not the directory's: every earlier run's report
// is still on disk, and summing them made the number grow run after run.
func TestTheCountIsTheNewestReportsOnly(t *testing.T) {
	dir := useReports(t)
	writeRows(t, dir, "2026-09-09", 12, 30*24*time.Hour)
	writeRows(t, dir, "2026-10-11", 12, 24*time.Hour)
	writeRows(t, dir, "2026-10-12", 5, time.Minute)
	if out := stdout(t, qualityCount); !strings.Contains(out, "the report has 5 row(s)") {
		t.Errorf("printed %q, want the newest report's 5 rows", out)
	}
}

// Another run's report is not this run's, however new: a backfill runs its
// days side by side, and the day after this one may finish its report a
// minute before this run counts.
func TestTheCountIsThisRunsDaysReport(t *testing.T) {
	dir := useReports(t)
	writeRows(t, dir, "2026-10-12", 5, time.Hour)
	writeRows(t, dir, "2026-10-13", 9, time.Minute)
	if out := stdout(t, qualityCount); !strings.Contains(out, "the report has 5 row(s)") {
		t.Errorf("printed %q, want this run's day's 5 rows", out)
	}
}

// And the freshness check judges that same report, not a newer one of
// another day standing in for it.
func TestAnotherDaysFreshReportDoesNotVouchForThisOne(t *testing.T) {
	dir := useReports(t)
	writeReport(t, dir, "2026-10-12", 30*time.Hour)
	writeReport(t, dir, "2026-10-13", time.Minute)
	if err := qualityFreshness(); err == nil {
		t.Fatal("accepted this day's thirty-hour-old report on another day's word")
	}
}

// With no report at all the count is zero, not a failure: that zero is what
// `unless_empty: count_rows.rows` reads to skip the freshness check, which is
// the shape this example exists to show.
func TestNoReportCountsZero(t *testing.T) {
	useReports(t)
	if out := stdout(t, qualityCount); !strings.Contains(out, "the report has 0 row(s)") {
		t.Errorf("printed %q, want 0 rows", out)
	}
}

// And the freshness check, given no report at all, still refuses: there is
// nothing current to vouch for.
func TestNoReportIsNotFresh(t *testing.T) {
	useReports(t)
	if err := qualityFreshness(); err == nil {
		t.Fatal("accepted an empty report directory")
	}
}

// useLanding points the staging directory at a fresh one for the test.
func useLanding(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := landing
	landing = dir
	t.Cleanup(func() { landing = old })
	return dir
}

// stage writes a staged file holding n rows landed by run.
func stage(t *testing.T, dir, name, run string, n int) {
	t.Helper()
	row := `{"partition":"2026-09-05","sku":"a","quantity":1,"price":2,"run_id":"` + run + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat(row, n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Staging keeps a day of files on purpose -- a retry of this run needs what
// its earlier attempt landed -- so an earlier run's rows are still there on
// the second run of the day. They are not this run's, and `check` used to
// count them anyway: 24 rows on the first run of a day, 64 a few runs later.
func TestCheckCountsOnlyThisRunsRows(t *testing.T) {
	dir := useLanding(t)
	t.Setenv("BREVIS_RUN_ID", "this-run")
	stage(t, dir, "earlier.ndjson", "earlier-run", 24)
	stage(t, dir, "mine.ndjson", "this-run", 8)
	// Written before rows carried their run: never this run's.
	if err := os.WriteFile(filepath.Join(dir, "old.ndjson"), []byte("{\"sku\":\"a\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := stdout(t, check); !strings.Contains(out, "8 row(s) of this run") {
		t.Errorf("printed %q, want this run's 8 rows", out)
	}
}

// The report's filter is the same rule, record by record.
func TestTheReportReadsOnlyThisRunsRows(t *testing.T) {
	keep := ofRun("this-run")
	if _, err := keep(map[string]any{"run_id": "this-run"}); err != nil {
		t.Errorf("dropped this run's row: %v", err)
	}
	for _, row := range []map[string]any{{"run_id": "earlier-run"}, {"sku": "a"}} {
		if _, err := keep(row); !errors.Is(err, sdk.SkipRecord) {
			t.Errorf("kept %v", row)
		}
	}
}
