package core

import (
	"fmt"
	"reflect"
	"strings"
	"time"
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
