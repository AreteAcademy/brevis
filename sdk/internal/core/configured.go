package core

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// Configured is a driver that says how it is set up, for the console's /data:
// the settings that decide what it reads or writes, in the order somebody
// would read them.
//
// AN ALLOWLIST, WRITTEN BY HAND. The driver lists what may be shown; nothing
// is read off its fields by reflection, because the same structs hold a DSN
// and a password and a reflected dump would publish them to every console
// login. Host, database, table, write mode: yes. DSN, password, token, a
// query string's values: never. The engine refuses a description carrying a
// credential (catalog.FindSecret), but that is the second wall, not this one.
//
// Optional, like Locator: a driver without it is described by its type alone.
type Configured interface {
	Configuration() []Setting
}

// Setting is one of a driver's settings. Value is a string, a number, a bool
// or a list of them; the key is snake_case, as the YAML of a gateway spells
// its own.
type Setting struct {
	Key   string
	Value any
}

// Keep is the settings that say something: a zero value -- an empty string, a
// false, a nil -- is a setting nobody chose, and listing it would bury the
// ones somebody did.
func Keep(settings ...Setting) []Setting {
	out := settings[:0]
	for _, s := range settings {
		if s.Value == nil {
			continue
		}
		if v := reflect.ValueOf(s.Value); v.IsZero() || (v.Kind() == reflect.Slice && v.Len() == 0) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// DurationText is a duration as somebody writes one: `1m`, `2160h`, `500ms`
// -- Go's spelling without the trailing zeroes.
func DurationText(d time.Duration) string {
	if d == 0 {
		return ""
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// EvolutionText is an Evolution as a setting: "" for EvolveNone, which is the
// zero value and what every load did before evolution existed.
func EvolutionText(e Evolution) string {
	switch e {
	case EvolveAdditive:
		return "additive"
	case EvolveAdditiveFromPayload:
		return "additive_from_payload"
	}
	return ""
}

// TypeName is a value's type as its package spells it -- `s3.Store` -- for a
// setting that is an implementation rather than a value. "" for nil.
func TypeName(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*")
}

// SetByFunction is what a function-valued setting says: that it is set, since
// what it does cannot be shown.
func SetByFunction(isSet bool) string {
	if isSet {
		return "set by a function"
	}
	return ""
}

// SafeURL is an address as a description may show it: scheme, host and path,
// with every place a credential lives taken out.
//
//   - userinfo, the query and the fragment are dropped whole;
//   - a matrix parameter (`;jsessionid=…`) is cut from its segment;
//   - a path segment that looks like a token is shown as `…`.
//
// "Looks like a token" is what bot APIs and webhooks put in a path --
// `/bot123456789:AAH…/`, `/services/T0…/B0…/Xx8kLmN3…` -- and not what a
// resource path is made of: it holds a `:`, or it is 16 characters or more
// mixing letters and digits, or 32 or more of anything. A long resource id
// is masked too, which costs a reader one id and never a secret.
//
// A raw address carrying an `@` that the parser did not read as userinfo
// gives "": an unescaped `#` in a password makes the parser take the user
// for the host, and what it would show is the credential.
//
// A location with no scheme -- a local path -- has its segments scrubbed the
// same way and is otherwise kept.
func SafeURL(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return scrubPath(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if strings.Contains(raw, "@") && u.User == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + scrubPath(u.Path)
}

// scrubPath masks the segments of a path that look like credentials.
func scrubPath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		s, _, _ = strings.Cut(s, ";")
		if looksLikeToken(s) {
			s = "…"
		}
		segments[i] = s
	}
	return strings.Join(segments, "/")
}

func looksLikeToken(s string) bool {
	if strings.Contains(s, ":") || len(s) >= 32 {
		return true
	}
	if len(s) < 16 {
		return false
	}
	var letter, digit bool
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return letter && digit
}
