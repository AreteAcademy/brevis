package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

type toggleSpy struct{ actor *string }

func (s toggleSpy) Toggle(_ context.Context, _ string, actor string) (bool, error) {
	*s.actor = actor
	return false, nil
}

func (toggleSpy) Disparar(context.Context, string, time.Time, map[string]string, *time.Time) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// THE PAUSE BUTTON SAYS WHO PRESSED IT: the signed-in operator reaches the
// schedule, and on a console with no login, nobody does.
func TestThePauseButtonPassesWhoPressedIt(t *testing.T) {
	for user, want := range map[string]string{"ana": "ana", "": ""} {
		actor := "unset"
		ui := api.NewUI(nil, nil, nil, toggleSpy{actor: &actor}, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
		mux := http.NewServeMux()
		ui.Registrar(mux)
		req := httptest.NewRequest(http.MethodPost, "/workflows/daily/toggle", nil)
		if user != "" {
			req = req.WithContext(auth.IntoContext(req.Context(), user))
		}
		mux.ServeHTTP(httptest.NewRecorder(), req)
		if actor != want {
			t.Errorf("signed in as %q, the schedule heard %q", user, actor)
		}
	}
}

func (toggleSpy) Cancel(context.Context, uuid.UUID, string) (postgres.CancelResult, error) {
	return postgres.CancelResult{}, nil
}
