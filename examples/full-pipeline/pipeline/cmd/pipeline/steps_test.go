package main

import (
	"os"
	"path/filepath"
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
