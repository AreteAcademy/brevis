package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func switchLang(t *testing.T, lang, next string) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	dataUI(catalogFake{}).Registrar(mux)
	form := url.Values{"lang": {lang}, "next": {next}}
	req := httptest.NewRequest(http.MethodPost, "/preferences/lang", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Result()
}

// CHOOSING A LANGUAGE IS A POST THAT REMEMBERS IT AND GOES BACK, with no
// script: the cookie for a year, and a 303 to the page the reader was on.
func TestChoosingALanguageRemembersItAndReturnsToThePage(t *testing.T) {
	res := switchLang(t, "pt-BR", "/runs?status=failed")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/runs?status=failed" {
		t.Fatalf("%d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	var got *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "brevis_lang" {
			got = c
		}
	}
	if got == nil || got.Value != "pt-BR" || got.Path != "/" || got.MaxAge < 365*24*3600 || got.SameSite != http.SameSiteLaxMode {
		t.Errorf("the cookie is %+v", got)
	}
}

// THE WAY BACK IS ONLY EVER THIS CONSOLE: `next` goes through auth.Target, the
// login's own guard, so the switcher cannot be made into an open redirect.
func TestTheWayBackNeverLeavesTheConsole(t *testing.T) {
	for _, next := range []string{"//evil.example", "https://evil.example/", `/\evil.example`, ""} {
		if loc := switchLang(t, "es", next).Header.Get("Location"); loc != "/" {
			t.Errorf("next %q sent the reader to %q", next, loc)
		}
	}
}

// A LANGUAGE THE CONSOLE DOES NOT SPEAK IS REFUSED, and nothing is written.
func TestAnUnknownLanguageIsRefused(t *testing.T) {
	res := switchLang(t, "it", "/runs")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", res.StatusCode)
	}
	if len(res.Cookies()) > 0 {
		t.Errorf("a cookie was written: %v", res.Cookies())
	}
}

// THE SWITCHER IS IN THE SIDEBAR'S FOOT: a form whose three buttons each
// choose a language -- no script needed -- that returns to this very page,
// query included, and marks the language in use.
func TestTheSidebarCarriesTheLanguageSwitcher(t *testing.T) {
	body := pageIn(t, dataUI(catalogFake{}), "/ingest/loads?kind=http", "es", "", false)
	aside, end := strings.Index(body, "<aside"), strings.Index(body, "</aside>")
	at := strings.Index(body, `action="/preferences/lang"`)
	if at < aside || at > end {
		t.Fatal("the sidebar has no language switcher")
	}
	form := body[at : at+strings.Index(body[at:], "</form>")]
	for _, want := range []string{
		`name="next" value="/ingest/loads?kind=http"`,
		`name="lang" value="en"`, `name="lang" value="pt-BR"`, `name="lang" value="es"`,
		">English<", ">Português<", ">Español<",
	} {
		if !strings.Contains(form, want) {
			t.Errorf("the switcher lacks %s", want)
		}
	}
	current := form[strings.Index(form, `value="es"`):]
	current = current[:strings.Index(current, ">")]
	if !strings.Contains(current, `aria-current="true"`) {
		t.Errorf("the language in use is not marked: %s", current)
	}
}

// AND THE PALETTE OFFERS THE SAME THREE, each named in its own language.
func TestThePaletteOffersEachLanguage(t *testing.T) {
	_, _, items := palette(t, dataUI(catalogFake{}))
	want := map[string]bool{"set-lang:en": false, "set-lang:pt-BR": false, "set-lang:es": false}
	for _, it := range items {
		if _, ok := want[it.Action]; ok && it.Group == "Actions" {
			want[it.Action] = true
		}
	}
	for a, seen := range want {
		if !seen {
			t.Errorf("the palette has no %s", a)
		}
	}
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	if !strings.Contains(strip(raw), `"set-lang:"`) {
		t.Error("ui.js does not act on a set-lang item")
	}
}
