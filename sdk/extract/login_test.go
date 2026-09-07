package extract

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// serverWithLogin trades a secret for a token and requires the token on the
// data endpoint.
func serverWithLogin(t *testing.T, failTimes int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var logins atomic.Int32
	var failures atomic.Int32
	failures.Store(failTimes)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		if failures.Add(-1) >= 0 {
			http.Error(w, "indisponível", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "meu-segredo") {
			http.Error(w, "sem o segredo no corpo", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "login precisa ser POST", http.StatusMethodNotAllowed)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"accessToken":"tok-123"}}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			http.Error(w, "sem token", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &logins
}

func readAll(t *testing.T, s core.Source) error {
	t.Helper()
	seq, err := JSON(context.Background(), s, nil)
	if err != nil {
		return err
	}
	for _, err := range seq {
		if err != nil {
			return err
		}
	}
	return nil
}

func loginCredential(srv *httptest.Server) *core.Credential {
	return &core.Credential{
		Login: &core.Login{
			URL:   srv.URL + "/oauth/token",
			Body:  core.JSONBody(map[string]any{"client_secret": "meu-segredo"}),
			Token: core.JSONToken("data.accessToken"),
		},
		Apply: core.AsBearer,
	}
}

// TestLoginTradesSecretsForAToken is item 9: the fetcher's most sensitive
// request stops being the only one without the others' guarantees.
func TestLoginTradesSecretsForAToken(t *testing.T) {
	srv, logins := serverWithLogin(t, 0)

	if err := readAll(t, core.Source{URL: srv.URL + "/dados", Auth: loginCredential(srv)}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if logins.Load() != 1 {
		t.Errorf("%d logins, esperado 1", logins.Load())
	}
}

// TestLoginHasRetry is the guarantee the hand-written version does not have. A
// 503 on the login took the whole run down; here it costs one retry, like any
// other request.
func TestLoginHasRetry(t *testing.T) {
	srv, logins := serverWithLogin(t, 2)

	err := readAll(t, core.Source{
		URL:  srv.URL + "/dados",
		Auth: loginCredential(srv),
		RetryConfig: &core.RetryConfig{
			MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("o login não teve retry: %v", err)
	}
	if logins.Load() != 3 {
		t.Errorf("%d tentativas de login, esperado 3", logins.Load())
	}
}

// TestLoginCachesWithTTL: some APIs rate-limit the FREQUENCY of authentication
// rather than that of requests.
func TestLoginCachesWithTTL(t *testing.T) {
	srv, logins := serverWithLogin(t, 0)
	cred := loginCredential(srv)
	cred.TTL = time.Hour

	for i := 0; i < 3; i++ {
		if _, err := cred.Get(context.Background()); err != nil {
			// The first call needs the client, which only exists inside the
			// fetch -- so the read comes first.
			_ = err
		}
	}
	if err := readAll(t, core.Source{URL: srv.URL + "/dados", Auth: cred}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := cred.Get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if logins.Load() != 1 {
		t.Errorf("%d logins para várias chamadas com TTL, esperado 1", logins.Load())
	}
}

// TestALoginThatFailsStopsTheRun: carrying on would send every page with an
// empty Authorization, and the error would come back blaming the data
// endpoint.
func TestALoginThatFailsStopsTheRun(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credencial recusada", http.StatusUnauthorized)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("pediu dados depois de o login falhar")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := readAll(t, core.Source{URL: srv.URL + "/dados", Auth: loginCredential(srv)})
	if err == nil {
		t.Fatal("o login falhou e a execução seguiu")
	}
	if !strings.Contains(err.Error(), "login") || !strings.Contains(err.Error(), "401") {
		t.Errorf("o erro não diz que foi o login: %v", err)
	}
}

// TestLoginDoesNotLeakTheSourcesHeader: the login endpoint may live on another
// host, and the source's header may carry a secret.
func TestLoginDoesNotLeakTheSourcesHeader(t *testing.T) {
	var vistoNoLogin string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		vistoNoLogin = r.Header.Get("X-Segredo-Da-Fonte")
		_, _ = fmt.Fprint(w, `{"data":{"accessToken":"tok-123"}}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := core.Source{
		URL:    srv.URL + "/dados",
		Header: map[string][]string{"X-Segredo-Da-Fonte": {"não deveria ir"}},
		Auth:   loginCredential(srv),
	}
	if err := readAll(t, source); err != nil {
		t.Fatal(err)
	}
	if vistoNoLogin != "" {
		t.Errorf("o cabeçalho da fonte foi para o login: %q", vistoNoLogin)
	}
}

// TestLoginRefusesConfigurationThatCannotWork.
func TestLoginRefusesConfigurationThatCannotWork(t *testing.T) {
	casos := []struct {
		name string
		cred *core.Credential
		diz  string
	}{
		{"Login e Value juntos", &core.Credential{
			Value: core.FromEnv("PATH"), Apply: core.AsBearer,
			Login: &core.Login{URL: "http://x", Token: core.JSONToken("t")},
		}, "both set"},
		{"Login sem URL", &core.Credential{
			Apply: core.AsBearer, Login: &core.Login{Token: core.JSONToken("t")},
		}, "URL"},
		{"Login sem Token", &core.Credential{
			Apply: core.AsBearer, Login: &core.Login{URL: "http://x"},
		}, "Token"},
		{"nem Value nem Login", &core.Credential{Apply: core.AsBearer}, "both nil"},
	}
	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			_, err := JSON(context.Background(), core.Source{URL: "http://x", Auth: c.cred}, nil)
			if err == nil {
				t.Fatal("passou")
			}
			if !strings.Contains(err.Error(), c.diz) {
				t.Errorf("o erro não diz %q: %v", c.diz, err)
			}
		})
	}
}

// TestAMissingJSONFieldIsAnError: an absent token would become an empty header
// and a 401 further down, blaming the API for a path this side wrote
// errado.
func TestAMissingJSONFieldIsAnError(t *testing.T) {
	_, err := core.JSONToken("data.accessToken")([]byte(`{"data":{"outro":"x"}}`))
	if err == nil {
		t.Fatal("campo ausente passou")
	}
	for _, quero := range []string{"accessToken", "empty header"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}
