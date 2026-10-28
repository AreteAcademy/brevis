package layouts

import (
	"context"
	"net/http"

	"github.com/AreteAcademy/brevis/web/i18n"
)

// ThemeCookie names the visitor's choice of theme.
//
// A COOKIE AND NOT localStorage, because the server is the one drawing the
// page: it writes `data-theme` on <html> before anything is painted, and a
// preference only a script can read is a page drawn dark and then repainted.
const ThemeCookie = "brevis_theme"

type themeKey struct{}

// WithTheme carries the request's theme to the layout, the way WithSQL does.
// Anything but "light" is dark, which is the console's default: a cookie
// written by an older console, or by hand, cannot leave the page unthemed.
func WithTheme(ctx context.Context, r *http.Request) context.Context {
	theme := "dark"
	if c, err := r.Cookie(ThemeCookie); err == nil && c.Value == "light" {
		theme = "light"
	}
	return context.WithValue(ctx, themeKey{}, theme)
}

// Theme reads it back; absent is dark.
func Theme(ctx context.Context) string {
	if t, _ := ctx.Value(themeKey{}).(string); t == "light" {
		return t
	}
	return "dark"
}

// themeSwitchLabel says which way the switch goes, since "toggle theme" does
// not.
func themeSwitchLabel(ctx context.Context) string {
	if Theme(ctx) == "light" {
		return i18n.T(ctx, "theme.to_dark")
	}
	return i18n.T(ctx, "theme.to_light")
}
