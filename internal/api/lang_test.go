package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
)

func pageIn(t *testing.T, ui *api.UI, path, cookie, accept string, login bool) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	if login {
		ui.RegistrarLogin(mux, nil)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "brevis_lang", Value: cookie})
	}
	if accept != "" {
		req.Header.Set("Accept-Language", accept)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Result().Body)
	return string(raw)
}

func htmlTag(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "<html")
	if i < 0 {
		t.Fatalf("no <html> in %.200s", body)
	}
	return body[i : i+strings.Index(body[i:], ">")]
}

// THE PAGE SAYS WHICH LANGUAGE IT IS IN, the one the server chose: a screen
// reader pronounces by it, and a browser offers to translate by it.
func TestThePageDeclaresTheLanguageItWasDrawnIn(t *testing.T) {
	ui := dataUI(catalogFake{})
	for _, c := range []struct{ cookie, accept, want string }{
		{"", "", `lang="en"`},
		{"pt-BR", "", `lang="pt-BR"`},
		// The browser's language is not used yet: see i18n.browserLanguage.
		{"", "es-MX,es;q=0.9", `lang="en"`},
	} {
		if tag := htmlTag(t, pageIn(t, ui, "/ingest/loads", c.cookie, c.accept, false)); !strings.Contains(tag, c.want) {
			t.Errorf("cookie %q, accept %q: %s, want %s", c.cookie, c.accept, tag, c.want)
		}
	}
}

// AND SO DOES THE SIGN-IN PAGE, which is the first one a reader sees.
func TestTheSignInPageDeclaresTheLanguageToo(t *testing.T) {
	tag := htmlTag(t, pageIn(t, dataUI(catalogFake{}), "/login", "pt-BR", "", true))
	if !strings.Contains(tag, `lang="pt-BR"`) {
		t.Errorf("the sign-in page is %s", tag)
	}
}
