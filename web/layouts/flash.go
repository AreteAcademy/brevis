package layouts

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// FlashCookie carries one sentence from a POST to the page after it.
//
// A POST that changes something answers with a redirect, and a redirect's
// body is never seen -- so "orders paused" travels in a cookie that the next
// page draws as a toast, in the HTML, and clears. No script is needed to see
// it, and a reload does not say it twice.
const FlashCookie = "brevis_flash"

// linkSep separates a flash's text from its link.
const linkSep = "\x1f"

// Flash is what the next page says.
type Flash struct {
	Kind    string // ok, fail or run
	Message string
	// Action and Href are the toast's link -- "Follow" to the run a POST
	// made. Both or neither.
	Action, Href string
}

// SetFlash has the next page say message. A kind it does not know is ok, the
// neutral confirmation, rather than a red toast nobody asked for.
func SetFlash(w http.ResponseWriter, kind, message string) {
	SetFlashLink(w, kind, message, "", "")
}

// SetFlashLink is SetFlash with a link in the toast. href is the server's
// own path, never the reader's input.
func SetFlashLink(w http.ResponseWriter, kind, message, action, href string) {
	v := flashKind(kind) + "|" + message
	if action != "" && href != "" {
		// The unit separator: a message can carry "|", and the link must not
		// be read out of one.
		v += linkSep + action + linkSep + href
	}
	http.SetCookie(w, &http.Cookie{
		Name: FlashCookie, Value: url.QueryEscape(v),
		Path: "/", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// TakeFlash reads the request's flash into the context and clears the
// cookie on the response, so it is said once.
func TakeFlash(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	f, ok := readFlash(r)
	if !ok {
		return ctx
	}
	http.SetCookie(w, &http.Cookie{Name: FlashCookie, Path: "/", MaxAge: -1})
	return context.WithValue(ctx, flashKey{}, f)
}

type flashKey struct{}

func flashOf(ctx context.Context) (Flash, bool) {
	f, ok := ctx.Value(flashKey{}).(Flash)
	return f, ok
}

func readFlash(r *http.Request) (Flash, bool) {
	c, err := r.Cookie(FlashCookie)
	if err != nil {
		return Flash{}, false
	}
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return Flash{}, false
	}
	text, link, _ := strings.Cut(v, linkSep)
	kind, msg, ok := strings.Cut(text, "|")
	if !ok || msg == "" {
		return Flash{}, false
	}
	f := Flash{Kind: flashKind(kind), Message: msg}
	// The link is drawn only to a path on this site: the cookie is the
	// server's, but a reader can set their own.
	if action, href, ok := strings.Cut(link, linkSep); ok && strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") {
		f.Action, f.Href = action, href
	}
	return f, true
}

func flashKind(k string) string {
	switch k {
	case "fail", "run":
		return k
	}
	return "ok"
}
