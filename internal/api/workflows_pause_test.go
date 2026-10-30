package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/layouts"
)

// flipper is a schedule that flips on each toggle, starting active.
type flipper struct{ active *bool }

func (f flipper) Toggle(context.Context, string, string) (bool, error) {
	*f.active = !*f.active
	return *f.active, nil
}

func (flipper) Disparar(context.Context, string, time.Time, map[string]string, *time.Time) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func (flipper) Cancel(context.Context, uuid.UUID, string) (postgres.CancelResult, error) {
	return postgres.CancelResult{}, nil
}

func toggle(t *testing.T, ui *api.UI, fragment bool) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodPost, "/workflows/daily/toggle", nil)
	r.Header.Set("Referer", "/workflows?state=paused")
	if fragment {
		r.Header.Set("X-Brevis-Fragment", "toggle")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// PAUSING FROM THE LIST ANSWERS THE SCRIPT IN A LINE: whether the schedule
// is now on, the sentence the toast says, and the word that undoes it -- in
// the reader's language, which the script does not have.
func TestPausingFromTheListAnswersTheScript(t *testing.T) {
	active := true
	ui := api.NewUI(nil, nil, nil, flipper{active: &active}, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
	rec := toggle(t, ui, true)
	var got struct {
		Active        bool
		Message, Undo string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the answer is not JSON: %v\n%s", err, rec.Body)
	}
	if got.Active || !strings.Contains(got.Message, "daily paused") || got.Undo != "Undo" {
		t.Errorf("pausing answered %+v", got)
	}
	if err := json.Unmarshal(toggle(t, ui, true).Body.Bytes(), &got); err != nil || !got.Active || !strings.Contains(got.Message, "daily resumed") {
		t.Errorf("resuming answered %+v (%v)", got, err)
	}
}

// WITHOUT A SCRIPT, THE PAGE COMES BACK SAYING WHAT CHANGED.
func TestPausingWithNoScriptComesBackWithAFlash(t *testing.T) {
	active := true
	ui := api.NewUI(nil, nil, nil, flipper{active: &active}, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
	rec := toggle(t, ui, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	var flash string
	for _, c := range rec.Result().Cookies() {
		if c.Name == layouts.FlashCookie {
			flash = c.Value
		}
	}
	if !strings.Contains(flash, "daily+paused") && !strings.Contains(flash, "daily%20paused") {
		t.Errorf("no flash says the schedule was paused: %q", flash)
	}
}

// THE SWITCH IS A SWITCH, and the row's data fades by CSS -- so the script
// flips both without redrawing the row.
func TestTheWorkflowsSwitchIsASwitch(t *testing.T) {
	_, body := get(t, columnsUI(nil), "/workflows")
	row := rowOf(t, body, "resting")
	for _, want := range []string{`data-toggle`, `role="switch"`, `aria-checked="false"`, `data-fade`} {
		if !strings.Contains(row, want) {
			t.Errorf("the paused row has no %s", want)
		}
	}
	if !strings.Contains(rowOf(t, body, "polyglot"), `aria-checked="true"`) {
		t.Error("an active switch is not checked")
	}
	_, js := get(t, columnsUI(nil), "/assets/ui.js")
	if !strings.Contains(strip(js), `form[data-toggle]`) {
		t.Error("ui.js does not post the switch")
	}
}

// HOW TO CREATE A WORKFLOW IS A DRAWER, opened from both of the list's
// actions: a workflow is born in the repository and published by the CLI,
// and the console says so rather than offering a form it does not have.
func TestHowToCreateAWorkflowIsADrawer(t *testing.T) {
	_, body := get(t, columnsUI(nil), "/workflows")
	if strings.Count(body, `data-dialogo="how-to-create"`) != 2 {
		t.Error("the list's two actions do not open the same drawer")
	}
	at := strings.Index(body, `<dialog id="how-to-create"`)
	if at < 0 {
		t.Fatal("there is no drawer")
	}
	drawer := body[at : at+strings.Index(body[at:], "</dialog>")]
	for _, want := range []string{"brevis validate", "brevis publish", `href="https://brevis.sh/docs/workflows/"`} {
		if !strings.Contains(drawer, want) {
			t.Errorf("the drawer has no %s", want)
		}
	}
}
