package extract

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// spyStore records what was written, touching neither disk nor cloud.
type spyStore struct {
	mu       sync.Mutex
	guardado string
	writes   int
}

func (s *spyStore) Load() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardado, nil
}

func (s *spyStore) Save(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guardado = v
	s.writes++
	return nil
}

func (s *spyStore) Describe() string { return "store espiao" }

func (s *spyStore) state() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardado, s.writes
}

// loggedOutSession imitates NextAuth for a session that did not authenticate:
// HTTP 200, a `null` body, and a Set-Cookie CLEARING the values. There is no
// error status at all -- the body is the only place the difference shows.
func loggedOutSession(t *testing.T, outCookie string) (*httptest.Server, *int) {
	t.Helper()
	var pages int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: outCookie})
		_, _ = fmt.Fprint(w, `null`)
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, _ *http.Request) {
		pages++
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &pages
}

func runWithStore(t *testing.T, srv *httptest.Server, store core.CredentialStore, expiresAt func([]byte) (time.Time, error)) error {
	t.Helper()
	seq, err := JSON(context.Background(), core.Source{
		URL: srv.URL + "/api/proxy/dados",
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=colado-por-um-humano", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:       srv.URL + "/api/auth/session",
				ExpiresAt: expiresAt,
				Store:     store,
			},
		},
	}, nil)
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

// TestARefreshThatDoesNotAuthenticateDoesNotWrite is §10 of SDK_V9.md.
//
// NextAuth answers 200 with a null body and a Set-Cookie EMPTYING the values for
// a session that did not authenticate. Writing that, with the read order being
// store-before-seed, means swapping the env var for a good credential stops
// fixing anything: the dead value always wins, and the only way out is deleting
// the object by hand. The symptom for whoever operates it is a 401 with no
// explanation.
func TestARefreshThatDoesNotAuthenticateDoesNotWrite(t *testing.T) {
	srv, _ := loggedOutSession(t, "")
	store := &spyStore{}

	err := runWithStore(t, srv, store, core.JSONField("expires"))
	if err == nil {
		t.Fatal("a renovacao nao autenticada devia falhar a execucao")
	}
	if !strings.Contains(err.Error(), "expires") {
		t.Errorf("o erro nao aponta a validade ausente: %v", err)
	}

	guardado, writes := store.state()
	if writes != 0 {
		t.Errorf("gravou %d vez(es) numa renovacao que nao autenticou; guardou %q",
			writes, guardado)
	}
}

// TestAGoodRefreshStillWrites: the other side of criterion 2. Without it the fix
// would become "it never writes", which resolves the defect and erases the
// feature.
func TestAGoodRefreshStillWrites(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotacionado"})
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := &spyStore{}
	if err := runWithStore(t, srv, store, core.JSONField("expires")); err != nil {
		t.Fatalf("execucao boa falhou: %v", err)
	}

	guardado, writes := store.state()
	if writes != 1 {
		t.Fatalf("gravacoes = %d, esperado 1", writes)
	}
	if !strings.Contains(guardado, "rotacionado") {
		t.Errorf("guardou %q, esperava o valor rotacionado", guardado)
	}
}

// TestWithoutExpiresAtItWrites: with a nil ExpiresAt the SDK has no signal at
// all that the refresh authenticated -- the status is 200 either way. So it
// writes, and the warning at assembly time is what tells whoever configured it
// what that costs.
func TestWithoutExpiresAtItWrites(t *testing.T) {
	srv, _ := loggedOutSession(t, "seja-la-o-que-for")
	store := &spyStore{}

	if err := runWithStore(t, srv, store, nil); err != nil {
		t.Fatalf("sem ExpiresAt a execucao devia seguir: %v", err)
	}
	if _, writes := store.state(); writes != 1 {
		t.Errorf("gravacoes = %d, esperado 1", writes)
	}
}

// TestTheRotationAppliesToThePagesEvenWhenExpiresAtFails is criterion 5:
// `applyRotation` does not move. The reissued credential has to apply to this
// run even when the validity does not arrive -- what changes is only when it
// is
// PERSISTIDA.
func TestTheRotationAppliesToThePagesEvenWhenExpiresAtFails(t *testing.T) {
	var mu sync.Mutex
	var cookieOnPage string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotacionado"})
		_, _ = fmt.Fprint(w, `null`) // no "expires"
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookieOnPage = r.Header.Get("Cookie")
		mu.Unlock()
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := core.Source{
		URL: srv.URL + "/api/proxy/dados",
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=velho", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL: srv.URL + "/api/auth/session",
				// Without ExpiresAt the run goes on, and that is where it becomes
				// observable
				// se a rotacao chegou as paginas.
			},
		},
	}
	seq, err := JSON(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(cookieOnPage, "rotacionado") {
		t.Errorf("a pagina foi com %q; a rotacao deixou de valer para a execucao", cookieOnPage)
	}
}

// TestAStoreWithoutExpiresAtWarnsAtAssembly is criterion 4. Not a refusal: there
// are sources whose refresh returns no validity, and for those the store is
// still worth having. But the
// limite tem de ser dito a quem configurou -- nessa combinacao o store
// stays poisonable, and nothing at runtime will reveal that.
func TestAStoreWithoutExpiresAtWarnsAtAssembly(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)

	c := &core.Credential{
		Value:   func(context.Context) (string, error) { return "x", nil },
		Apply:   core.AsBearer,
		Refresh: &core.Refresh{URL: "http://x", Store: &spyStore{}},
	}
	if err := c.Check(); err != nil {
		t.Fatalf("virou erro em vez de aviso: %v", err)
	}

	log := buf.String()
	for _, exigido := range []string{"ExpiresAt", "store espiao", "200"} {
		if !strings.Contains(log, exigido) {
			t.Errorf("o aviso nao diz %q:\n%s", exigido, log)
		}
	}
}

// TestAStoreWithExpiresAtDoesNotWarn: a warning that appears on the right
// configuration
// ensina a ignorar avisos.
func TestAStoreWithExpiresAtDoesNotWarn(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)

	c := &core.Credential{
		Value: func(context.Context) (string, error) { return "x", nil },
		Apply: core.AsBearer,
		Refresh: &core.Refresh{
			URL: "http://x", Store: &spyStore{}, ExpiresAt: core.JSONField("expires"),
		},
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "without ExpiresAt") {
		t.Errorf("avisou na configuracao certa:\n%s", buf.String())
	}
}
