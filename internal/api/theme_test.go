package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getWithTheme is get() with the visitor's theme cookie, "" for none.
func getWithTheme(t *testing.T, cookie string) string {
	t.Helper()
	mux := http.NewServeMux()
	dataUI(catalogFake{}).Registrar(mux)
	req := httptest.NewRequest(http.MethodGet, "/ingest/loads", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "brevis_theme", Value: cookie})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

// THE SERVER CHOOSES THE THEME, so the first paint is the right one: a
// script that read a preference after the HTML arrived would draw the page
// dark and then repaint it, on every navigation.
func TestTheThemeIsTheCookiesAndDarkByDefault(t *testing.T) {
	for cookie, want := range map[string]string{
		"":       `data-theme="dark"`,
		"light":  `data-theme="light"`,
		"dark":   `data-theme="dark"`,
		"purple": `data-theme="dark"`,
	} {
		body := getWithTheme(t, cookie)
		i := strings.Index(body, "<html")
		if i < 0 {
			t.Fatalf("cookie %q: the page has no <html>:\n%.300s", cookie, body)
		}
		html := body[i:]
		html = html[:strings.Index(html, ">")]
		if !strings.Contains(html, want) {
			t.Errorf("cookie %q: <html> is %s, want %s", cookie, html, want)
		}
	}
}

// THE THEME IS CHOSEN IN THE ACCOUNT MENU: auto, light, dark -- three
// icon buttons, each named for a screen reader -- and the choice is
// localStorage's, read by the first script in the head, before the paint.
func TestTheAccountMenuOffersAutoLightAndDark(t *testing.T) {
	body := getWithTheme(t, "")
	for _, want := range []string{
		`data-theme-set="auto"`, `data-theme-set="light"`, `data-theme-set="dark"`,
		`aria-label="Auto"`, `aria-label="Light"`, `aria-label="Dark"`,
		`localStorage.getItem("brevis_theme")`, `localStorage.getItem("brevis_lang")`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if strings.Contains(body, "data-theme-toggle") {
		t.Error("the old toggle is still drawn")
	}
}
