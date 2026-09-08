package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/notify"
)

type alertsFake struct {
	rows []alerts.Record
	err  error
}

func (a alertsFake) ForRun(context.Context, uuid.UUID) ([]alerts.Record, error) {
	return a.rows, a.err
}

// page renders /runs/<id> and returns the HTML.
func page(t *testing.T, raised api.AlertsReader) (int, string) {
	t.Helper()
	id := uuid.New()
	ui := api.NewUI(nil, nil,
		execsFake{run: dom.Run{ID: id, WorkflowSlug: "id_verification", Status: dom.StatusFailed}},
		nil, raised, branding.Default(), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+id.String(), nil))

	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func record(state string, attempts int, failure string) alerts.Record {
	now := time.Now()
	r := alerts.Record{
		Item: alerts.Item{
			ID: 1, RunID: uuid.New(), Kind: alerts.KindRun,
			Channel: alerts.ChannelSlack, Attempts: attempts,
			Payload: notify.Alert{Workflow: "id_verification"},
		},
		Err: failure, CreatedAt: now,
	}
	switch state {
	case "delivered":
		r.DeliveredAt = &now
	case "undelivered":
		r.GaveUpAt = &now
	}
	return r
}

// TestAnUndeliveredAlertIsOnTheScreen is what keeps this feature honest.
//
// An alert nobody can see is indistinguishable from an alert that was never
// sent, and "raised, not delivered, 4 tries, 403 from Slack" is the single most
// useful thing the outbox produces: it is the case where somebody is waiting
// for a message that is not coming. Before the outbox, that state was a log
// line in a pod that no longer exists.
func TestAnUndeliveredAlertIsOnTheScreen(t *testing.T) {
	status, html := page(t, alertsFake{rows: []alerts.Record{
		record("undelivered", 4, "slack respondeu 403: invalid_token"),
	}})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	for _, want := range []string{
		"never delivered",
		"4 tries",
		"403: invalid_token",
		alerts.ChannelSlack,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
}

// A delivered alert says so, and shows no error: it succeeded on the third try
// and must not still display the second try's failure.
func TestADeliveredAlertCarriesNoStaleError(t *testing.T) {
	_, html := page(t, alertsFake{rows: []alerts.Record{record("delivered", 3, "")}})
	if !strings.Contains(html, "delivered") {
		t.Error("a delivered alert does not say so")
	}
	if strings.Contains(html, "never delivered") {
		t.Error("a delivered alert reads as undelivered")
	}
}

// TestAnAlertNobodyIsDrainingSaysWhichProcessIsMissing.
//
// "sending" on its own is ambiguous in exactly the way that matters: it is also
// what every row looks like when `brevis alert` is not running. An installation
// that upgraded the scheduler and forgot the alert pod records every alert
// correctly and delivers none, and this line is what makes that findable from
// the screen instead of from a metric nobody set up yet.
func TestAnAlertNobodyIsDrainingSaysWhichProcessIsMissing(t *testing.T) {
	_, html := page(t, alertsFake{rows: []alerts.Record{record("sending", 0, "")}})
	if !strings.Contains(html, "brevis alert") {
		t.Errorf("a pending alert does not name the process that delivers it")
	}
}

// A run with no alerts renders no section at all -- an empty "Alerts" heading on
// every successful run would be noise on the screen people look at most.
func TestARunWithNoAlertsShowsNoSection(t *testing.T) {
	_, html := page(t, alertsFake{})
	if strings.Contains(html, ">Alerts<") {
		t.Error("an empty alerts section was rendered")
	}
}

// The alerts being unreadable must not take the page down. It is the same rule
// the logs already follow: a blank page is worse at showing what happened than
// a page missing one block.
func TestTheRunPageSurvivesTheAlertsBeingUnavailable(t *testing.T) {
	status, html := page(t, alertsFake{err: context.DeadlineExceeded})
	if status != http.StatusOK {
		t.Fatalf("status = %d: a failed alerts read took the page down", status)
	}
	if !strings.Contains(html, "id_verification") {
		t.Error("the page rendered without the run on it")
	}
}

// TestTheAutoParamsAreOnTheScreen.
//
// A value a pipeline reads is a value somebody debugging that pipeline has to
// be able to see. "Why did this run fetch that window" is answered here rather
// than by reading the fetcher's source and reconstructing its arithmetic.
func TestTheAutoParamsAreOnTheScreen(t *testing.T) {
	slot := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	started := slot.Add(37 * time.Minute)
	before := slot.AddDate(0, 0, -1)

	id := uuid.New()
	ui := api.NewUI(nil, nil, execsFake{run: dom.Run{
		ID: id, WorkflowSlug: "nightly", Status: dom.StatusSuccess,
		Auto: dom.AutoParams{
			ScheduledAt: &slot, StartedAt: &started, AdjustedAt: slot,
			DelaySeconds: 37 * 60, Date: "2026-09-08",
			IntervalStart: &before, IntervalEnd: &slot,
			PreviousError: true, PreviousSuccessAt: &before,
		},
	}}, nil, alertsFake{}, branding.Default(), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+id.String(), nil))
	html := rec.Body.String()

	for _, want := range []string{
		"Auto params",
		"adjusted_at", "interval_start", "interval_end",
		"previous_error", "previous_success_at",
		"2026-09-08",  // the date
		"BREVIS_AUTO", // how a step reads them
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// The delay reads as a duration, not as a count of seconds: "2220" is a
	// number somebody converts, "37m" is an answer.
	if strings.Contains(html, "2220") {
		t.Errorf("the delay is shown in raw seconds")
	}
	if !strings.Contains(html, "37m") {
		t.Errorf("the delay does not read as a duration:\n%s", html)
	}
}

// A manual run has no slot and no window, and the screen shows no empty rows
// for them: a field that is always there and sometimes blank is a field
// everybody learns to skip.
func TestAManualRunShowsOnlyWhatItHas(t *testing.T) {
	started := time.Date(2026, 9, 8, 11, 3, 0, 0, time.UTC)
	id := uuid.New()
	ui := api.NewUI(nil, nil, execsFake{run: dom.Run{
		ID: id, WorkflowSlug: "on_demand", Status: dom.StatusSuccess, TriggerType: "manual",
		Auto: dom.AutoParams{StartedAt: &started, AdjustedAt: started, Date: "2026-09-08"},
	}}, nil, alertsFake{}, branding.Default(), slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+id.String(), nil))
	html := rec.Body.String()

	for _, absent := range []string{"scheduled_at", "interval_start", "previous_success_at"} {
		if strings.Contains(html, absent) {
			t.Errorf("the page shows %q on a run that has none", absent)
		}
	}
	if !strings.Contains(html, "on time") {
		t.Errorf("a manual run's delay does not read as `on time`")
	}
}
