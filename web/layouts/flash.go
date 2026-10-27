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

// Flash is what the next page says.
type Flash struct {
	Kind    string // ok, fail or run
	Message string
}

// SetFlash has the next page say message. A kind it does not know is ok, the
// neutral confirmation, rather than a red toast nobody asked for.
func SetFlash(w http.ResponseWriter, kind, message string) {
	http.SetCookie(w, &http.Cookie{
		Name: FlashCookie, Value: url.QueryEscape(flashKind(kind) + "|" + message),
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
	kind, msg, ok := strings.Cut(v, "|")
	if !ok || msg == "" {
		return Flash{}, false
	}
	return Flash{Kind: flashKind(kind), Message: msg}, true
}

func flashKind(k string) string {
	switch k {
	case "fail", "run":
		return k
	}
	return "ok"
}
