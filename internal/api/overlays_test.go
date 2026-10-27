package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A FLASH IS A TOAST THE NEXT PAGE DRAWS. A POST that redirects loses its
// response, so the sentence travels in a cookie the following page reads,
// draws in the toast region -- with no script needed to see it -- and
// clears, so a reload does not say it again.
func TestAFlashBecomesAToastOnTheNextPageAndIsCleared(t *testing.T) {
	mux := http.NewServeMux()
	dataUI(catalogFake{}).Registrar(mux)
	req := httptest.NewRequest(http.MethodGet, "/ingest/loads", nil)
	req.AddCookie(&http.Cookie{Name: "brevis_flash", Value: url.QueryEscape("ok|orders paused <b>now</b>")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Result().Body)
	body := string(raw)

	region := strings.Index(body, `id="toasts"`)
	if region < 0 {
		t.Fatal("the page has no toast region")
	}
	if !strings.Contains(body[strings.LastIndex(body[:region], "<"):region+200], `role="status"`) {
		t.Error("the toast region is not a status region, so nobody hears it")
	}
	msg := strings.Index(body, "orders paused &lt;b&gt;now&lt;/b&gt;")
	if msg < region {
		t.Errorf("the flash is not drawn in the toast region, escaped")
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "brevis_flash" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the flash cookie is not cleared, so a reload says it again")
	}
}

// WITHOUT ONE, THE REGION IS THERE AND EMPTY, for a script's toasts.
func TestWithoutAFlashTheToastRegionIsEmpty(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")
	at := strings.Index(body, `id="toasts"`)
	if at < 0 {
		t.Fatal("the page has no toast region")
	}
	if strings.Contains(body[at:at+strings.Index(body[at:], "</div>")], `class="toast"`) {
		t.Error("a toast was drawn with no flash")
	}
}

// THE SCRIPT HALF: what only a browser can show is checked by hand (the
// checkpoint's script), and what the file must CONTAIN is checked here.
func TestTheOverlayScriptReturnsFocusAndTimesToasts(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{
		`addEventListener("close"`, // a dialog gives the focus back when it closes
		`[data-dialog-body]`,       // and opens on its first field, not its close button
		`window.Brevis`,            // Brevis.toast for the screens' own scripts
		`4200`,                     // a toast leaves after 4.2s
		`[role="switch"]`,          // a switch flips aria-checked
	} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js never uses %s", want)
		}
	}
}
