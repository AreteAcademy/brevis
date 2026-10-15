package assets_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
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

// A VERSIONED URL IS CACHED FOREVER, AND ONLY THE CURRENT VERSION.
//
// `no-cache` costs one round trip per asset per page: locally nothing, over a
// 50-100ms link a blank pause on every click before the page can paint. The
// URL now carries the content's digest, so a file that changes gets a new URL
// and the old one may be kept for a year -- "a fix ships and arrives" still
// holds, by a different road. A `v` that is not the current digest (a page
// from before an upgrade) gets `no-cache`, as before: a year-long promise is
// made only about the bytes actually served.
func TestAVersionedURLIsCachedForeverAndOnlyTheCurrentOne(t *testing.T) {
	srv := http.StripPrefix("/assets/", assets.Handler())
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	u := assets.URL("dag.js")
	if !strings.HasPrefix(u, "/assets/dag.js?v=") {
		t.Fatalf("URL(dag.js) = %q, which carries no version", u)
	}
	cur := get(u)
	if cur.Code != http.StatusOK || cur.Body.Len() == 0 {
		t.Fatalf("GET %s = %d", u, cur.Code)
	}
	if cc := cur.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("the current version is not cached for good: %q", cc)
	}
	if cc := get("/assets/dag.js?v=0000stale").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("a stale version is promised a year: %q", cc)
	}
	if assets.URL("dag.js") == assets.URL("sql.js") {
		t.Error("two different files share a versioned URL")
	}
}

// THE SHEET ASKS FOR ITS FONTS BY VERSION TOO. Tailwind writes
// `url(/assets/fonts/…)` and cannot know a digest, so the served app.css is
// rewritten once at start: otherwise the four fonts would be revalidated on
// every page while everything else is cached.
func TestTheStylesheetAsksForItsFontsByVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	http.StripPrefix("/assets/", assets.Handler()).ServeHTTP(rec, httptest.NewRequest("GET", assets.URL("app.css"), nil))
	css := rec.Body.String()
	urls := regexp.MustCompile(`url\((/assets/fonts/[^)]+)\)`).FindAllStringSubmatch(css, -1)
	if len(urls) < 4 {
		t.Fatalf("found %d font URLs in the served sheet", len(urls))
	}
	for _, m := range urls {
		if !strings.Contains(m[1], "?v=") {
			t.Errorf("the sheet asks for %s with no version", m[1])
		}
	}
}
