package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestTheRefreshGetsTheCredentialUnderAnotherPrefix pins what the first
// consumer hit on v0.9.x.
//
// AsCookie seeds the jar from the SOURCE's URL, and Go's cookiejar, when the
// cookie carries no Path, uses that URL's directory. With the source on
// /api/proxy/... and the refresh on /api/auth/session, the jar sends nothing --
// the endpoint answers null for unauthenticated, ExpiresAt does not find
// "expires", and the run dies before the first page.
//
// The test that existed used srv.URL + "/dados", whose directory is "/", which
// matches everything. It passed because the source was at the root, and no real
// API is.
func TestTheRefreshGetsTheCredentialUnderAnotherPrefix(t *testing.T) {
	casos := []struct {
		name    string
		source  string
		renovar string
	}{
		{"prefixos diferentes", "/api/proxy/occurrences", "/api/auth/session"},
		{"mesmo prefixo", "/api/proxy/occurrences", "/api/proxy/session"},
		{"fonte na raiz", "/dados", "/auth/session"},
		{"renovacao mais funda", "/api/dados", "/api/v2/interno/auth/session"},
	}

	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var cookieOnRefresh string
			var cookieOnPages []string

			mux := http.NewServeMux()
			mux.HandleFunc(c.renovar, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				cookieOnRefresh = r.Header.Get("Cookie")
				mu.Unlock()

				// Like the real API: with no credential, it answers null.
				if _, err := r.Cookie("session"); err != nil {
					_, _ = fmt.Fprint(w, `null`)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "renovado=="})
				_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
			})
			mux.HandleFunc(c.source, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				cookieOnPages = append(cookieOnPages, r.Header.Get("Cookie"))
				mu.Unlock()
				_, _ = fmt.Fprint(w, `{"ok":1}`)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			var stats core.Stats
			seq, err := JSON(context.Background(), core.Source{
				URL:   srv.URL + c.source,
				Stats: &stats,
				Auth: &core.Credential{
					Value: func(context.Context) (string, error) { return "session=colado==", nil },
					Apply: core.AsCookie,
					Refresh: &core.Refresh{
						URL:       srv.URL + c.renovar,
						ExpiresAt: core.JSONField("expires"),
					},
				},
			}, nil)
			if err != nil {
				t.Fatalf("a execucao morreu: %v", err)
			}
			for _, err := range seq {
				if err != nil {
					t.Fatalf("iterando: %v", err)
				}
			}

			if cookieOnRefresh == "" {
				t.Error("a renovacao foi SEM a credencial")
			}
			// And the REISSUED cookie has to apply to the pages, or the refresh
			// refreshed for nobody: the new value would stay pinned to the
			// refresh URL's directory and the pages would carry on with the old
			// one, which is the same defect in the opposite direction.
			//
			// The test server reissues WITHOUT Path, on purpose: RFC 6265's
			// default is the case that breaks.
			for _, got := range cookieOnPages {
				if got == "" {
					t.Error("a pagina foi sem credencial nenhuma")
				}
				if !strings.Contains(got, "renovado==") {
					t.Errorf("a pagina foi com %q; esperava o valor reemitido pela renovacao", got)
				}
			}
			if stats.CredentialExpiry.IsZero() {
				t.Error("a validade nao chegou; a renovacao nao autenticou")
			}
		})
	}
}
