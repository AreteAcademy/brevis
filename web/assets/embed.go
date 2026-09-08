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

import "embed"

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
