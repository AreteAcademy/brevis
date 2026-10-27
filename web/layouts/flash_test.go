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
