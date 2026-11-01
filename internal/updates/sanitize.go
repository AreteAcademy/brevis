package updates

import (
	"html"
	"regexp"
	"strings"
)

var tag = regexp.MustCompile(`<(/?)([a-zA-Z][a-zA-Z0-9]*)([^<>]*)>`)

var href = regexp.MustCompile(`(?i)\shref\s*=\s*"(https?://[^"]*)"`)

// allowed are the tags a release's notes may use. Anything else loses its tag
// and keeps its text; every attribute is dropped but an http(s) href.
var allowed = map[string]bool{
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"p": true, "br": true, "hr": true, "ul": true, "ol": true, "li": true,
	"strong": true, "b": true, "em": true, "i": true, "del": true, "code": true,
	"pre": true, "blockquote": true, "a": true, "table": true, "thead": true,
	"tbody": true, "tr": true, "th": true, "td": true,
}

// Sanitize makes GitHub's rendered release notes safe to draw inside the
// console. GitHub already sanitises them; this is the second wall, for the
// day that stops being true: scripts, styles, images, events and
// javascript: links cannot get through an allowlist.
func Sanitize(in string) string {
	var out strings.Builder
	last := 0
	text := func(s string) {
		// A stray "<" is text, not markup.
		out.WriteString(strings.ReplaceAll(s, "<", "&lt;"))
	}
	for _, m := range tag.FindAllStringSubmatchIndex(in, -1) {
		text(in[last:m[0]])
		last = m[1]
		closing, name, attrs := in[m[2]:m[3]], strings.ToLower(in[m[4]:m[5]]), in[m[6]:m[7]]
		if !allowed[name] {
			continue
		}
		switch {
		case closing == "/":
			out.WriteString("</" + name + ">")
		case name == "a":
			if h := href.FindStringSubmatch(attrs); h != nil {
				out.WriteString(`<a href="` + html.EscapeString(html.UnescapeString(h[1])) + `" target="_blank" rel="noopener noreferrer">`)
			} else {
				out.WriteString("<a>")
			}
		default:
			out.WriteString("<" + name + ">")
		}
	}
	text(in[last:])
	return out.String()
}
