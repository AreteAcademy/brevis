package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/auth"
)

func credential(t *testing.T, user, password string) auth.Credential {
	t.Helper()
	h, err := auth.GenerateHash(password)
	if err != nil {
		t.Fatal(err)
	}
	return auth.Credential{
		User: user, Hash: h,
		Secret: []byte("um-segredo-de-teste-com-mais-de-32-bytes"),
	}
}

func TestTheHashAcceptsTheRightPassword(t *testing.T) {
	h, err := auth.GenerateHash("senha-correta-longa")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(h, "senha-correta-longa") {
		t.Error("the right password was refused")
	}
	if auth.CheckPassword(h, "senha-errada-longa!") {
		t.Error("the wrong password was accepted")
	}
}

// Two hashes of the SAME password have to differ: with no salt, equal hashes in
// the database
// entregam quais operadores escolheram a mesma senha.
func TestTheHashUsesARandomSalt(t *testing.T) {
	a, _ := auth.GenerateHash("mesma-senha-aqui")
	b, _ := auth.GenerateHash("mesma-senha-aqui")
	if a == b {
		t.Error("two hashes of the same password came out equal -- there is no salt")
	}
}

// A malformed hash never "passes": the error path of a password checker is
// exactly where a bug becomes an open door.
func TestAMalformedHashDoesNotAuthenticate(t *testing.T) {
	for _, h := range []string{
		"", "abc", "pbkdf2-sha256$", "pbkdf2-sha256$0$c2Fs$Y2hhdmU",
		"pbkdf2-sha256$600000$!!!$!!!", "md5$1$x$y",
		"pbkdf2-sha256$600000$c2Fs$", // empty key
	} {
		if auth.CheckPassword(h, "qualquer-coisa") {
			t.Errorf("hash malformado %q autenticou", h)
		}
		if auth.CheckPassword(h, "") {
			t.Errorf("malformed hash %q authenticated with an empty password", h)
		}
	}
}

// This is the test that stands for the incident: in dev, an anonymous POST to
// /workflows/<slug>/trigger answered 303 and fired a `dbt build` that writes into
// the data warehouse.
func TestAnAnonymousTriggerIsBlocked(t *testing.T) {
	gate := &auth.Gate{
		Cred: credential(t, "operador", "senha-de-teste-longa"),
		Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("the anonymous request reached the protected handler")
			w.WriteHeader(http.StatusOK)
		}),
	}

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest("POST", "/workflows/id_verification/trigger", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; esperava 401", rec.Code)
	}
}

func TestGetAnonimoVaiParaOLogin(t *testing.T) {
	gate := &auth.Gate{
		Cred: credential(t, "operador", "senha-de-teste-longa"),
		Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest("GET", "/runs?pagina=2", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; esperava 303", rec.Code)
	}
	if target := rec.Header().Get("Location"); !strings.Contains(target, "/runs") {
		t.Errorf("Location = %q; it should carry the original destination", target)
	}
}

// Kubernetes's probes have to get through: a /health asking for a password takes
// the
// pod em ciclo, e o operador procura o problema no lugar errado.
func TestProbesAndAssetsPassWithNoSession(t *testing.T) {
	var arrived []string
	gate := &auth.Gate{
		Cred: credential(t, "operador", "senha-de-teste-longa"),
		Next: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			arrived = append(arrived, r.URL.Path)
		}),
	}
	for _, path := range []string{"/health", "/ready", "/assets/app.css", "/assets/fonts/x.woff2"} {
		gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if len(arrived) != 4 {
		t.Errorf("%v got through; expected the four free routes", arrived)
	}
}

func TestAValidSessionPassesAndCarriesTheUser(t *testing.T) {
	cred := credential(t, "operador", "senha-de-teste-longa")
	var visto string
	gate := &auth.Gate{
		Cred: cred,
		Next: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			visto = auth.De(r.Context())
		}),
	}

	login := httptest.NewRecorder()
	if !gate.SignIn(login, "operador", "senha-de-teste-longa") {
		t.Fatal("the right credential was refused")
	}
	cookie := login.Result().Cookies()[0]

	req := httptest.NewRequest("GET", "/runs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if visto != "operador" {
		t.Errorf("operador no contexto = %q; esperava %q", visto, "operador")
	}
	if rec.Code == http.StatusSeeOther {
		t.Error("the valid session was sent to the login")
	}
}

// A forged cookie must not count. If the signature were not checked, the
// cliente escreveria o proprio nome de usuario e a propria validade.
func TestAForgedCookieDoesNotGetIn(t *testing.T) {
	cred := credential(t, "operador", "senha-de-teste-longa")
	gate := &auth.Gate{
		Cred: cred,
		Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("a forged cookie got past the gate")
			w.WriteHeader(http.StatusOK)
		}),
	}
	for _, value := range []string{
		"operador|99999999999|qualquer-assinatura",
		"operador|99999999999|",
		"operador|99999999999",
		"outro|99999999999|",
		"",
	} {
		req := httptest.NewRequest("GET", "/runs", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: value})
		gate.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// Trocar o segredo derruba as sessoes — e a alavanca de emergencia.
func TestChangingTheSecretInvalidatesSessions(t *testing.T) {
	cred := credential(t, "operador", "senha-de-teste-longa")
	emissor := &auth.Gate{Cred: cred, Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	rec := httptest.NewRecorder()
	emissor.SignIn(rec, "operador", "senha-de-teste-longa")
	cookie := rec.Result().Cookies()[0]

	novo := cred
	novo.Secret = []byte("outro-segredo-completamente-diferente-32")
	gate := &auth.Gate{
		Cred: novo,
		Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("the session survived the secret being changed")
		}),
	}
	req := httptest.NewRequest("GET", "/runs", nil)
	req.AddCookie(cookie)
	gate.ServeHTTP(httptest.NewRecorder(), req)
}

// `/login?next=https://malicious` must not send the operator off-site.
func TestAnExternalDestinationIsDiscarded(t *testing.T) {
	for bruto, esperado := range map[string]string{
		"https://malicioso.example": "/",
		"//malicioso.example":       "/",
		"/runs?pagina=2":            "/runs?pagina=2",
		"":                          "/",
	} {
		if d := auth.Target(bruto); d != esperado {
			t.Errorf("Target(%q) = %q; esperava %q", bruto, d, esperado)
		}
	}
}

func TestAHalfCredentialIsRefused(t *testing.T) {
	if err := (auth.Credential{User: "operador"}).Validate(); err == nil {
		t.Error("a user with no hash was accepted -- the door would stay open believing it had shut")
	}
	if err := (auth.Credential{Hash: "pbkdf2-sha256$1$a$b"}).Validate(); err == nil {
		t.Error("a hash with no user was accepted")
	}
	curto := auth.Credential{User: "o", Hash: "pbkdf2-sha256$1$a$b", Secret: []byte("curto")}
	if err := curto.Validate(); err == nil {
		t.Error("a secret that is too short was accepted")
	}
}
