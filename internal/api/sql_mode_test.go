package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// screen opens /sql on a target and returns what was drawn.
func screen(t *testing.T, ui interface{ Registrar(*http.ServeMux) }, target string) string {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, "/sql?target="+target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("/sql answered %d", rec.Code)
	}
	return rec.Body.String()
}

// A WORKBENCH THAT MAY CHANGE THE WAREHOUSE SAYS SO, ON THE SCREEN.
//
// The whole plan for this mode is twenty lines of unlocking and everything
// else is about making it loud. The dangerous event is not somebody typing
// DROP here on purpose -- it is somebody who does not know this console can,
// because the last one they used could not.
func TestTheWorkbenchSaysWhenItMayChangeTheWarehouse(t *testing.T) {
	w, ui := browsing(t)
	w.objects = `{"relations":[{"schema":"bronze","name":"orders"}],"writable":true}`
	body := screen(t, ui, probeTarget)

	if !strings.Contains(body, "data-writable") {
		t.Error("the workbench does not mark itself as writable")
	}
	// AND IN WORDS, not only in an attribute a script reads. A banner that
	// exists only for the island is a banner nobody sees without JavaScript.
	if !strings.Contains(body, "change data") {
		t.Errorf("nothing on the screen says what this console may do")
	}
}

// AND A READ-ONLY WORKBENCH SAYS NOTHING, which is the default and every
// deployment that has not asked. A warning on every screen is a warning
// nobody reads.
func TestAReadOnlyWorkbenchDrawsNoBanner(t *testing.T) {
	w, ui := browsing(t)
	w.objects = `{"relations":[{"schema":"bronze","name":"orders"}]}`
	body := screen(t, ui, probeTarget)

	if strings.Contains(body, "data-writable") {
		t.Error("a read-only workbench warned about writes")
	}
	if strings.Contains(body, "change data") {
		t.Error("a read-only workbench says it may change data")
	}
}

// topBar is the first <header>: the shell's bar, not the page's.
func topBar(body string) string {
	at := strings.Index(body, "<header")
	if at < 0 {
		return ""
	}
	return body[at : at+strings.Index(body[at:], "</header>")]
}

// A WRITABLE WORKBENCH SAYS SO IN THE TOP BAR TOO, which stays on screen
// however the panes are arranged. A read-only one puts nothing there -- the
// same rule as the banner: read-only is the default, and a badge on every
// visit is one nobody reads.
func TestAWritableWorkbenchSaysSoInTheTopBar(t *testing.T) {
	w, ui := browsing(t)
	w.objects = `{"relations":[{"schema":"bronze","name":"orders"}],"writable":true}`
	if bar := topBar(screen(t, ui, probeTarget)); !strings.Contains(bar, "Writes allowed") {
		t.Errorf("the top bar of a writable workbench does not say so: %s", bar)
	}

	w.objects = `{"relations":[{"schema":"bronze","name":"orders"}]}`
	if bar := topBar(screen(t, ui, probeTarget)); strings.Contains(bar, "Writes allowed") || strings.Contains(bar, "data-mode") {
		t.Error("a read-only workbench put a mode in the top bar")
	}
}

// AND IT DOES NOT ALSO SAY "READS ONLY". The sentence under the editor was
// drawn on every connection, so a writable one said both that statements may
// change data and that the service refuses anything but a read.
func TestAWritableWorkbenchDoesNotClaimToReadOnly(t *testing.T) {
	w, ui := browsing(t)
	w.objects = `{"relations":[{"schema":"bronze","name":"orders"}],"writable":true}`
	if strings.Contains(screen(t, ui, probeTarget), "Reads only:") {
		t.Error("a writable workbench claims to read only")
	}
	w.objects = `{"relations":[{"schema":"bronze","name":"orders"}]}`
	if !strings.Contains(screen(t, ui, probeTarget), "Reads only:") {
		t.Error("a read-only workbench no longer says what it refuses")
	}
}
