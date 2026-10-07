// Package assets embeds the static files into the binary.
//
// Serving from the filesystem (`http.Dir("web/assets")`) broke in two
// situations: in the distroless container, which copies only the binary, and
// when running `brevis` from any directory other than the repo's root. In both
// the CSS 404'd and the UI came up unstyled — which looks like a defect in the
// page, not in the path.
//
// Embedding solves both at once, and follows what is already done with the
// migrations: the binary carries everything it needs.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// FS holds the compiled CSS and the React island's scripts. `app.src.css` is
// deliberately left out — it is Tailwind's input, not a served artifact.
//
// `vendor/` keeps React, ReactDOM and React Flow as UMD builds (~350 KB).
// Vendoring rather than pointing at a CDN is section 15's choice: no npm in the
// build, no external network dependency at runtime, and the UI keeps working in
// a cluster with no route to the internet.
//
// The fonts (IBM Plex Sans and Mono, ~92 KB) go in for the same reason as the
// bundles: a UI that depends on Google Fonts changes typeface halfway down the
// screen when the network does not answer.
//
//go:embed app.css ui.js dag.js jsx-shim.js logo.svg vendor fonts
var FS embed.FS

// LogoSVG is the default mark, already read out of the embedded FS.
//
// It exists as a string, and not only as a served file, because the symbol is
// drawn in `currentColor`: inside an <img> the SVG is an isolated document and
// inherits no colour from the page, so it would come out black under any theme.
// Inline, it follows the client's palette.
var LogoSVG = func() string {
	b, err := FS.ReadFile("logo.svg")
	if err != nil {
		// Impossible in production: the file is embedded in the binary and the
		// compilation fails without it. Empty degrades to "no logo".
		return ""
	}
	return string(b)
}()

// Handler serves the embedded files with a validator, which http.FileServerFS
// alone does not give them.
//
// An embedded file's ModTime is the zero time, so the stdlib's file server
// omits Last-Modified — and it sets no ETag of its own. The response therefore
// carried NO validator at all: not Cache-Control, not ETag, not Last-Modified.
// A browser then caches on a heuristic and a proxy may keep the file for as
// long as it likes, with no way to revalidate.
//
// That is how a UI fix ships and does not arrive. `/assets/dag.js` has no
// version in its path, so the upgraded binary serves new bytes at a URL some
// client is convinced it already has.
//
// The tag is the CONTENT's digest, not the build's version: it needs no ldflags
// reaching this package, and it changes exactly when the bytes change — a
// rebuild that alters nothing does not invalidate a single cache.
//
// `no-cache` is not "do not store". It stores, and revalidates every time, so
// the steady state is a 304 with no body. http.ServeContent reads the ETag that
// is already on the header and answers If-None-Match itself.
func Handler() http.Handler {
	files := http.FileServerFS(FS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := etags[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// etags is every embedded file's digest, computed once at start rather than per
// request: the set is fixed at compile time and hashing 350 KB of vendor
// bundles on every page load would be work with a known answer.
var etags = func() map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		out[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	return out
}()
