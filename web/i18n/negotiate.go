package i18n

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Cookie holds the reader's choice of language, set from the sidebar's
// selector or the palette.
const Cookie = "brevis_lang"

// Negotiate is the request's language: the cookie when it names one the
// console speaks, else the best of the browser's Accept-Language, else
// English.
//
// A REGION IS A HINT, NOT A REQUIREMENT. pt-PT reads the Brazilian catalog
// far better than English, and es-MX the Spanish one, so a tag is matched by
// its language alone; `q=0` refuses a language, as the header means.
func Negotiate(r *http.Request) string {
	if c, err := r.Cookie(Cookie); err == nil {
		for _, l := range Langs {
			if c.Value == l {
				return l
			}
		}
	}
	type choice struct {
		lang string
		q    float64
	}
	var choices []choice
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if lang := byLanguage(tag); lang != "" && q > 0 {
			choices = append(choices, choice{lang, q})
		}
	}
	// Stable, so equal weights keep the browser's order.
	sort.SliceStable(choices, func(a, b int) bool { return choices[a].q > choices[b].q })
	if len(choices) > 0 {
		return choices[0].lang
	}
	return "en"
}

// byLanguage maps a language tag to the catalog that speaks it, by its first
// subtag; "" for one the console does not speak.
func byLanguage(tag string) string {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	switch primary {
	case "pt":
		return "pt-BR"
	case "es":
		return "es"
	case "en":
		return "en"
	}
	return ""
}

// Names are each language in itself -- "Português", not "Portuguese" -- so a
// reader finds their own whatever the page is in.
var Names = map[string]string{"en": "English", "pt-BR": "Português", "es": "Español"}

// Codes are the short form the sidebar shows.
var Codes = map[string]string{"en": "EN", "pt-BR": "PT", "es": "ES"}

// Remember writes the reader's choice for a year, and says whether lang is
// one the console speaks; it writes nothing when it is not.
func Remember(w http.ResponseWriter, lang string) bool {
	if _, ok := Names[lang]; !ok {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: Cookie, Value: lang, Path: "/",
		MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode,
	})
	return true
}
