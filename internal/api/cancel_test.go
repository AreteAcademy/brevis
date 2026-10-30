package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// cancelSpy is the run repository as the cancel button sees it.
type cancelSpy struct {
	id    uuid.UUID
	actor string
	err   error
}

func (*cancelSpy) Toggle(context.Context, string, string) (bool, error) { return false, nil }
func (*cancelSpy) Disparar(context.Context, string, time.Time, map[string]string, *time.Time) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (s *cancelSpy) Cancel(_ context.Context, id uuid.UUID, actor string) (postgres.CancelResult, error) {
	s.id, s.actor = id, actor
	return postgres.CancelResult{Outcome: postgres.CancelRequested, Status: dom.StatusRunning}, s.err
}

func cancelMux(spy *cancelSpy) *http.ServeMux {
	ui := api.NewUI(nil, nil, nil, spy, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	ui.Registrar(mux)
	return mux
}

// THE CANCEL BUTTON SAYS WHO PRESSED IT, and comes back to the run, which now
// says what became of it.
func TestTheCancelButtonPassesWhoPressedIt(t *testing.T) {
	for user, want := range map[string]string{"ana": "ana", "": ""} {
		spy := &cancelSpy{actor: "unset"}
		id := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/runs/"+id.String()+"/cancel", nil)
		if user != "" {
			req = req.WithContext(auth.IntoContext(req.Context(), user))
		}
		rec := httptest.NewRecorder()
		cancelMux(spy).ServeHTTP(rec, req)

		if spy.id != id || spy.actor != want {
			t.Errorf("signed in as %q: canceled %s as %q; wanted %s as %q", user, spy.id, spy.actor, id, want)
		}
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/runs/"+id.String() {
			t.Errorf("answer %d to %q; wanted 303 to the run", rec.Code, loc)
		}
	}
}

// A run that does not exist, or an id that is not one, is a 404 and not a 500.
func TestCancelingNoRunIsNotFound(t *testing.T) {
	for path, err := range map[string]error{
		"/runs/" + uuid.NewString() + "/cancel": fmt.Errorf("run: %w", postgres.ErrRunNotFound),
		"/runs/not-an-id/cancel":                nil,
	} {
		rec := httptest.NewRecorder()
		cancelMux(&cancelSpy{err: err}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, wanted 404", path, rec.Code)
		}
	}
}
