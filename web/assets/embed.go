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
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
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
// AN EXPLICIT LIST, and that is a trap worth naming: a new file under this
// directory is NOT served until its name is added here, and the only sign is
// a 404 at runtime. `/assets/sql.js` shipped that way for a few minutes while
// four tests agreed the page referenced it.
//
// The list stays explicit anyway -- `embed *` would sweep up `app.src.css`,
// every `_test.go` beside it and whatever somebody drops here next. What
// catches the omission now is a test that walks a rendered page and GETs
// everything it asks for.
//
//go:embed app.css ui.js dag.js jsx-shim.js sql.js logo.svg favicon.svg vendor fonts
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

// Handler serves the embedded files with a validator AND, when the URL names
// the current version, with a year-long cache.
//
// THE VALIDATOR CAME FIRST. An embedded file's ModTime is the zero time, so
// the stdlib's file server omits Last-Modified and sets no ETag: the response
// carried no validator at all, a browser cached on a heuristic, and a UI fix
// shipped without arriving -- `/assets/dag.js` had no version in its path, so
// an upgraded binary served new bytes at a URL some client was convinced it
// already had. The tag is the CONTENT's digest, so it changes exactly when the
// bytes do, and a `no-cache` response is a 304 with no body in steady state.
//
// THE VERSIONED URL CAME SECOND, because `no-cache` still costs a round trip
// per asset per page: nothing locally, a blank pause on every click over a
// 50-100ms link. URL(name) puts the digest in the query, so a changed file
// gets a new URL and the old one can be kept for a year. Only a `v` that IS
// the current digest gets `immutable`; anything else -- no `v`, or a page
// from before an upgrade asking for the old one -- gets `no-cache`, as
// before. A year-long promise is made only about the bytes being served.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		f, ok := served[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"`+f.digest+`"`)
		if v := r.URL.Query().Get("v"); v != "" && v == f.version() {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// ServeContent reads the ETag already on the header and answers
		// If-None-Match itself; the zero time sends no Last-Modified.
		http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(f.body))
	})
}

// URL is the address of an embedded file at its current version:
// `/assets/app.css?v=<digest>`. Every reference a page makes goes through it,
// which is what lets the handler cache it for good. A name that is not
// embedded comes back unversioned, and the page's test says which.
func URL(name string) string {
	f, ok := served[name]
	if !ok {
		return "/assets/" + name
	}
	return "/assets/" + name + "?v=" + f.version()
}

type file struct {
	body   []byte
	digest string
}

// version is the digest, shortened: enough to tell versions apart, short
// enough to read in a URL.
func (f file) version() string { return f.digest[:12] }

// fontURL is how Tailwind writes a font into the compiled sheet.
var fontURL = regexp.MustCompile(`url\((/assets/(fonts/[^)?]+))\)`)

// served is every embedded file as it is SERVED, with its digest, computed
// once at start: the set is fixed at compile time, and hashing 350 KB of
// vendor bundles on every request would be work with a known answer.
//
// ONE FILE IS NOT SERVED AS EMBEDDED. Tailwind writes the fonts into app.css
// as `url(/assets/fonts/…)` and cannot know a digest, so the sheet is
// rewritten here to ask for each font by URL() -- otherwise the four fonts
// would be revalidated on every page while everything else is cached. The
// sheet's digest is taken AFTER the rewrite, so a changed font changes the
// sheet's URL too.
var served = func() map[string]file {
	out := map[string]file{}
	_ = fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return nil
		}
		out[p] = file{body: b, digest: digest(b)}
		return nil
	})
	if css, ok := out["app.css"]; ok {
		body := fontURL.ReplaceAllFunc(css.body, func(m []byte) []byte {
			name := string(fontURL.FindSubmatch(m)[2])
			f, ok := out[name]
			if !ok {
				return m
			}
			return []byte("url(/assets/" + name + "?v=" + f.version() + ")")
		})
		out["app.css"] = file{body: body, digest: digest(body)}
	}
	return out
}()

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
