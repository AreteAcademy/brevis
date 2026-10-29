package layouts

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A HANDLER SAYS WHAT HAPPENED IN ONE CALL, and a kind it does not know is
// the neutral one rather than a red toast nobody asked for.
func TestSetFlashWritesWhatTheNextPageReads(t *testing.T) {
	for kind, want := range map[string]string{"fail": "fail", "run": "run", "ok": "ok", "purple": "ok"} {
		rec := httptest.NewRecorder()
		SetFlash(rec, kind, "orders | paused")
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		f, ok := readFlash(req)
		if !ok || f.Kind != want || f.Message != "orders | paused" {
			t.Errorf("SetFlash(%q) read back as %+v, %v", kind, f, ok)
		}
	}
}

// A flash can carry a link -- "Follow" to the run a POST made -- and a "|"
// in its message does not break it; a link off this site is not drawn.
func TestAFlashCarriesItsLink(t *testing.T) {
	read := func(set func(http.ResponseWriter)) Flash {
		rec := httptest.NewRecorder()
		set(rec)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		f, _ := readFlash(req)
		return f
	}
	f := read(func(w http.ResponseWriter) { SetFlashLink(w, "run", "Rerun of a | b created", "Follow", "/runs/1") })
	if f.Message != "Rerun of a | b created" || f.Action != "Follow" || f.Href != "/runs/1" {
		t.Errorf("read back %+v", f)
	}
	f = read(func(w http.ResponseWriter) { SetFlashLink(w, "run", "x", "Follow", "//evil.example") })
	if f.Href != "" {
		t.Errorf("a link off this site is drawn: %+v", f)
	}
}
