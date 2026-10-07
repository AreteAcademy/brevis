package assets_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	assets "github.com/AreteAcademy/brevis/web/assets"
)

// The served response has to carry a validator. Without one there is no way for
// a client to ask "is my copy still good", and a UI fix ships without arriving:
// /assets/dag.js has no version in its path, so an upgraded binary serves new
// bytes at a URL the browser believes it already holds.
//
// http.FileServerFS alone gave none: an embedded file's ModTime is zero, so it
// omits Last-Modified, and it sets no ETag. This test exists because that was
// found in production, on a card that kept drawing the old layout.
func TestTheAssetsCanBeRevalidated(t *testing.T) {
	srv := http.StripPrefix("/assets/", assets.Handler())

	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest("GET", "/assets/dag.js", nil))

	if first.Code != http.StatusOK {
		t.Fatalf("GET dag.js = %d, want 200", first.Code)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Error("no ETag: the client cannot revalidate, so it guesses")
	}
	if got := first.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want \"no-cache\" -- store it, but always ask", got)
	}
	if first.Body.Len() == 0 {
		t.Error("empty body")
	}

	// The point of the tag: the second request costs no bytes.
	again := httptest.NewRequest("GET", "/assets/dag.js", nil)
	again.Header.Set("If-None-Match", tag)
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, again)

	if second.Code != http.StatusNotModified {
		t.Errorf("with If-None-Match: %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes", second.Body.Len())
	}

	// A tag that never changes is worse than no tag: the browser would hold the
	// old file for as long as the tag says it is current. The digest is over
	// the CONTENT, so two different files cannot share one.
	css := httptest.NewRecorder()
	srv.ServeHTTP(css, httptest.NewRequest("GET", "/assets/app.css", nil))
	if other := css.Header().Get("ETag"); other == tag {
		t.Errorf("dag.js and app.css share the ETag %s", tag)
	}
}
