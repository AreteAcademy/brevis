package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func useReports(t *testing.T) string {
	t.Helper()
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
