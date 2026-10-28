package i18n

import (
	"net/http/httptest"
	"testing"
)

// THE COOKIE IS THE READER'S CHOICE, THE HEADER IS THEIR BROWSER'S GUESS, AND
// ENGLISH IS WHAT IS LEFT. A Portuguese from Portugal reads Brazilian
// Portuguese better than English, and a Mexican reads the Spanish catalog; a
// language the console does not speak is never a blank page.
func TestNegotiateTakesTheCookieThenTheBrowserThenEnglish(t *testing.T) {
	for _, c := range []struct {
		cookie, accept, want string
	}{
		{"", "", "en"},
		{"pt-BR", "es", "pt-BR"},
		{"es", "", "es"},
		{"klingon", "pt-BR", "pt-BR"},
		{"", "pt-PT,pt;q=0.9", "pt-BR"},
		{"", "es-MX,es;q=0.9,en;q=0.8", "es"},
		{"", "fr-FR,fr;q=0.9", "en"},
		{"", "fr;q=0.9,es;q=0.5", "es"},
		{"", "en;q=0.1,pt;q=0.8", "pt-BR"},
		{"", "pt;q=0", "en"},
		{"", "*", "en"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.cookie != "" {
			r.Header.Add("Cookie", Cookie+"="+c.cookie)
		}
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if got := Negotiate(r); got != c.want {
			t.Errorf("cookie %q, Accept-Language %q: %s, want %s", c.cookie, c.accept, got, c.want)
		}
	}
}
