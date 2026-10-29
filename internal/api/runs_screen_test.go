package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// screenExecs answers Get for any of a set of runs, and nothing for the rest.
type screenExecs struct {
	execsFake
	runs map[uuid.UUID]dom.Run
}

func (e screenExecs) Get(_ context.Context, id uuid.UUID) (dom.Run, error) {
	if r, ok := e.runs[id]; ok {
		return r, nil
	}
	return dom.Run{}, pgx.ErrNoRows
}

var (
	newest = uuid.MustParse("11111111-0000-0000-0000-000000000001")
	older  = uuid.MustParse("11111111-0000-0000-0000-000000000002")
	beyond = uuid.MustParse("11111111-0000-0000-0000-000000000003")
)

func screenUI() *api.UI {
	slot := time.Date(2026, 10, 26, 22, 18, 0, 0, time.UTC)
	reads := runsReads{runs: []postgres.RunSummary{
		{ID: newest.String(), WorkflowSlug: "vendors_fire_inpe", Status: "running", LogicalDate: &slot, CreatedAt: time.Now()},
		{ID: older.String(), WorkflowSlug: "environment_alert", Status: "failed", LogicalDate: &slot,
			CreatedAt: time.Now().Add(-time.Minute), Duration: dur(12200 * time.Millisecond)},
	}}
	execs := screenExecs{runs: map[uuid.UUID]dom.Run{
		newest: {ID: newest, WorkflowSlug: "vendors_fire_inpe", Status: dom.StatusRunning},
		older:  {ID: older, WorkflowSlug: "environment_alert", Status: dom.StatusFailed},
		beyond: {ID: beyond, WorkflowSlug: "id_analytics", Status: dom.StatusSuccess},
	}}
	return api.NewUI(reads, nil, execs, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

// selected is the run the list marks, and detail the one the right side shows.
func selected(body string) (list, detail string) {
	if m := regexp.MustCompile(`data-run-id="([^"]+)"[^>]*aria-current="true"`).FindStringSubmatch(body); m != nil {
		list = m[1]
	}
	if m := regexp.MustCompile(`data-run-detail="([^"]+)"`).FindStringSubmatch(body); m != nil {
		detail = m[1]
	}
	return list, detail
}

// ONE SCREEN, TWO ADDRESSES. /runs is the list with the newest run open; the
// list is a column beside it, and each pane scrolls on its own.
func TestTheRunsScreenOpensTheNewestRun(t *testing.T) {
	code, body := get(t, screenUI(), "/runs")
	if code != http.StatusOK {
		t.Fatalf("/runs answered %d", code)
	}
	if !strings.Contains(body, "data-fill") || !strings.Contains(body, "data-runs-list") {
		t.Fatal("the runs screen is not a list column filling the shell")
	}
	if list, detail := selected(body); list != newest.String() || detail != newest.String() {
		t.Errorf("selected %q in the list and %q in the detail; wanted the newest, %s", list, detail, newest)
	}
}

// /runs/{id} is the same screen with that run open -- the address alerts and
// old links carry.
func TestARunsAddressOpensThatRun(t *testing.T) {
	_, body := get(t, screenUI(), "/runs/"+older.String())
	if !strings.Contains(body, "data-runs-list") {
		t.Fatal("the run's address lost the list")
	}
	if list, detail := selected(body); list != older.String() || detail != older.String() {
		t.Errorf("selected %q in the list and %q in the detail; wanted %s", list, detail, older)
	}
	if !strings.Contains(body, `href="/runs/`+older.String()+`"`) {
		t.Error("a list item is not a link to its run")
	}
}

// A run that is not on this page of the list is still the one opened: an
// alert from last week has to land on its run, not on whatever ran last.
func TestARunOffTheListIsStillOpened(t *testing.T) {
	code, body := get(t, screenUI(), "/runs/"+beyond.String())
	if code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if list, detail := selected(body); list != "" || detail != beyond.String() {
		t.Errorf("selected %q in the list and %q in the detail; wanted nothing and %s", list, detail, beyond)
	}
}

func TestAnUnknownRunIsNotFound(t *testing.T) {
	if code, _ := get(t, screenUI(), "/runs/"+uuid.NewString()); code != http.StatusNotFound {
		t.Errorf("answered %d, wanted 404", code)
	}
}

func TestNoRunsIsAnEmptyScreen(t *testing.T) {
	_, body := get(t, runsUI(runsReads{}), "/runs")
	if _, detail := selected(body); detail != "" {
		t.Errorf("a run is open with no runs: %s", detail)
	}
	if !strings.Contains(body, "data-runs-list") {
		t.Error("the empty screen lost the list column")
	}
}

// AN ITEM IS A LINE TO SCAN: the state's shape, the workflow, how long ago,
// the slot's hour and how long it took -- or that it is still running.
func TestARunsItemSaysWhatAndWhen(t *testing.T) {
	_, body := get(t, screenUI(), "/runs")
	item := func(id uuid.UUID) string {
		at := strings.Index(body, `data-run-id="`+id.String()+`"`)
		if at < 0 {
			t.Fatalf("no item for %s", id)
		}
		end := strings.Index(body[at:], "</a>")
		return body[at : at+end]
	}
	running, failed := item(newest), item(older)
	for _, want := range []string{"vendors_fire_inpe", "22:18", "running…"} {
		if !strings.Contains(running, want) {
			t.Errorf("the running item lacks %q:\n%s", want, running)
		}
	}
	for _, want := range []string{"environment_alert", "22:18", "12.2s", "1 min ago"} {
		if !strings.Contains(failed, want) {
			t.Errorf("the failed item lacks %q:\n%s", want, failed)
		}
	}
	if !strings.Contains(failed, `data-status="failed"`) {
		t.Errorf("the failed item has no state mark:\n%s", failed)
	}
}
