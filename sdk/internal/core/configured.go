package core

import (
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
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
// where every part is one KNOWN to be safe to show and anything else is `…`.
//
// AN ALLOWLIST, like the rest of a description. The first version listed what
// looked like a token instead -- a `:`, 16 characters mixing letters and
// digits -- and a review got Slack webhooks with no digit, 31-letter tokens,
// tokens in a host label and percent-encoded slashes past it. Nothing has to
// look like a secret to be hidden now; a part is shown only when it looks
// like what addresses are made of (see safePart).
//
//   - userinfo, the query and the fragment are never shown;
//   - a matrix parameter (`;jsessionid=…`) is cut from its segment;
//   - a host label or a path segment that is not plainly safe is `…`;
//   - an `@` in the authority that the parser did not read as userinfo --
//     an unescaped `#` in a password does that -- gives "", and so does an
//     `@` in an address with no scheme: an authority nobody parsed.
//
// Segments are judged ESCAPED, so `%2F` inside a token is part of the token
// and not a separator that would show its first half.
func SafeURL(raw string) string {
	if raw == "" {
		return ""
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		if strings.Contains(raw, "@") {
			return ""
		}
		return safeSegments(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || scheme == "" {
		return ""
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority = rest[:i]
	}
	if strings.Contains(authority, "@") && u.User == nil {
		return ""
	}
	host := safeHost(u.Hostname())
	if host == "" && u.Scheme != "file" {
		return ""
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host + safeSegments(u.EscapedPath())
}

// safeHost is a host with each label that is not plainly safe shown as `…`.
// An IP address is shown as it is.
func safeHost(h string) string {
	if ip := net.ParseIP(h); ip != nil {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	labels := strings.Split(h, ".")
	for i, l := range labels {
		if !safeSegment(l) {
			labels[i] = "…"
		}
	}
	return strings.Join(labels, ".")
}

// safeSegments is a path with each segment that is not plainly safe shown as
// `…`, and its matrix parameters cut.
func safeSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		s, _, _ = strings.Cut(s, ";")
		if !safeSegment(s) {
			s = "…"
		}
		segments[i] = s
	}
	return strings.Join(segments, "/")
}

var (
	uuidShape    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	dateShape    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	versionShape = regexp.MustCompile(`^v\d+(\.\d+)*$`)
	partSplit    = regexp.MustCompile(`[-_.~]`)
)

// safeSegment says whether a host label or a path segment is plainly safe to
// show: empty, `.` or `..`, a UUID, a date, a version, or made of parts
// (split on `-`, `_`, `.`, `~`) that are each plainly a word or a number --
// and with no more than two capitals across them all, which is a camelCase
// word and not a base64 run cut by separators.
func safeSegment(s string) bool {
	switch {
	case s == "", s == ".", s == "..":
		return true
	case len(s) > 40:
		return false
	case uuidShape.MatchString(s), dateShape.MatchString(s), versionShape.MatchString(s):
		return true
	}
	capitals := 0
	for _, part := range partSplit.Split(s, -1) {
		kind, ok := safePart(part)
		if !ok {
			return false
		}
		if kind == partWord {
			for _, r := range part {
				if unicode.IsUpper(r) {
					capitals++
				}
			}
		}
	}
	return capitals <= 2
}

type partKind int

const (
	partWord partKind = iota
	partOther
)

// safePart is one part of a segment that is plainly not a credential: a word
// of up to 20 letters, a number of up to 12 digits, an acronym of up to six
// capitals, or up to six letters and digits mixed (`v1`, `s3`, `94b6`).
// Anything else -- longer, other characters -- is not shown.
func safePart(p string) (partKind, bool) {
	if p == "" {
		return partOther, true
	}
	var letters, digits, upper int
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z':
			letters++
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
		case r >= '0' && r <= '9':
			digits++
		default:
			return partOther, false
		}
	}
	switch {
	case digits == 0 && upper == letters:
		return partOther, len(p) <= 6
	case digits == 0:
		return partWord, len(p) <= 20
	case letters == 0:
		return partOther, len(p) <= 12
	}
	return partOther, len(p) <= 6
}
