package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AreteAcademy/brevis/sql/internal/api/render"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// Param is one query-string parameter an endpoint takes.
//
// FIVE TYPES AND EACH ONE NARROW. A parameter is a filter somebody outside
// sends, and every type is the smallest set of strings that can mean what it
// says: an int is base-10 digits, a date is YYYY-MM-DD and nothing an
// instant-parser would also accept, a bool is `true` or `false`, an enum is
// one of the values the file lists. Anything looser is a second way to spell
// a value, and the cache key, the audit line and the author all read the
// first.
type Param struct {
	Type string `yaml:"type"`

	// MaxLen bounds a string, in characters. Required for one.
	MaxLen int `yaml:"max_len"`

	// Min and Max bound an int.
	Min *int64 `yaml:"min"`
	Max *int64 `yaml:"max"`

	// Values are an enum's, each an identifier: an enum is the one value
	// written into the SQL text, and an identifier is what makes that safe.
	Values []string `yaml:"values"`

	// Default stands in for an omitted value, and is parsed exactly as a
	// sent one is -- so a default its own type cannot hold is refused at
	// load.
	Default *string `yaml:"default"`

	// Required refuses a request that omits the parameter.
	Required bool `yaml:"required"`

	// Ref names a definition in the file's `params:`, and takes nothing
	// else.
	Ref string `yaml:"ref"`
}

const (
	typeString = "string"
	typeInt    = "int"
	typeDate   = "date"
	typeBool   = "bool"
	typeEnum   = "enum"
)

var types = []string{typeString, typeInt, typeDate, typeBool, typeEnum, typePeriod}

// check refuses a declaration on its own terms. A field that does not apply
// to the type is refused rather than ignored: `max_len` on an int is a bound
// somebody believes is there.
func (p Param) check() error {
	known := false
	for _, t := range types {
		known = known || p.Type == t
	}
	if !known {
		return fmt.Errorf("the type is %q; the types are: %s", p.Type, strings.Join(types, ", "))
	}
	if p.Type != typeString && p.MaxLen != 0 {
		return fmt.Errorf("`max_len` applies to a string only")
	}
	if p.Type != typeInt && (p.Min != nil || p.Max != nil) {
		return fmt.Errorf("`min` and `max` apply to an int only")
	}
	if p.Type != typeEnum && len(p.Values) > 0 {
		return fmt.Errorf("`values` applies to an enum only")
	}
	switch p.Type {
	case typeString:
		if p.MaxLen < 1 || p.MaxLen > maxStringLen {
			return fmt.Errorf("a string needs a `max_len` from 1 to %d", maxStringLen)
		}
	case typeInt:
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("`min` is above `max`")
		}
	case typeDate, typeBool, typePeriod:
	case typeEnum:
		if len(p.Values) == 0 {
			return fmt.Errorf("an enum needs `values`")
		}
		seen := map[string]bool{}
		for _, v := range p.Values {
			if !render.IsIdentifier(v) {
				return fmt.Errorf("the enum value %q is not an identifier (letters, digits and _): "+
					"it is written into the SQL", v)
			}
			if seen[v] {
				return fmt.Errorf("the enum value %q is listed twice", v)
			}
			seen[v] = true
		}
	}
	if p.Required && p.Default != nil {
		return fmt.Errorf("`required` and `default` together: a parameter with a default is never missing")
	}
	if p.Default != nil {
		if _, err := p.parse("default", *p.Default); err != nil {
			return fmt.Errorf("the `default` is not a valid %s: %v", p.Type, err)
		}
	}
	return nil
}

// parse turns one sent string into what the template binds.
//
// THE VALUE IS NEVER IN THE ERROR. It is the caller's own, so echoing it
// would tell them nothing -- and an error is also an audit line, which holds
// parameter names and never values.
func (p Param) parse(name, raw string) (render.Value, error) {
	switch p.Type {
	case typeString:
		if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
			return render.Value{}, fmt.Errorf("the parameter %s is not valid UTF-8 text", name)
		}
		if n := utf8.RuneCountInString(raw); n > p.MaxLen {
			return render.Value{}, fmt.Errorf("the parameter %s is %d characters long; at most %d", name, n, p.MaxLen)
		}
		return render.Bind(raw), nil
	case typeInt:
		// strconv and not fmt.Sscan: base 10, no sign games beyond one
		// leading '-', no `0x`, no `1e3`, no surrounding space.
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return render.Value{}, fmt.Errorf("the parameter %s is not a whole number in base 10", name)
		}
		if p.Min != nil && n < *p.Min {
			return render.Value{}, fmt.Errorf("the parameter %s is %d; at least %d", name, n, *p.Min)
		}
		if p.Max != nil && n > *p.Max {
			return render.Value{}, fmt.Errorf("the parameter %s is %d; at most %d", name, n, *p.Max)
		}
		return render.Bind(n), nil
	case typeDate:
		d, err := parseDate(raw)
		if err != nil {
			return render.Value{}, fmt.Errorf("the parameter %s is not a date: YYYY-MM-DD", name)
		}
		return render.Bind(d), nil
	case typeBool:
		switch raw {
		case "true":
			return render.Bind(true), nil
		case "false":
			return render.Bind(false), nil
		}
		return render.Value{}, fmt.Errorf("the parameter %s is true or false", name)
	case typeEnum:
		for _, v := range p.Values {
			if raw == v {
				return render.Inline(v), nil
			}
		}
		return render.Value{}, fmt.Errorf("the parameter %s is one of: %s", name, strings.Join(p.Values, ", "))
	case typePeriod:
		// CHECKED HERE, RESOLVED BY THE ENDPOINT: a period is not a value
		// the SQL binds but two dates it sets, and only the endpoint knows
		// the clock and the timezone.
		for _, v := range periods {
			if raw == v {
				return render.Value{}, nil
			}
		}
		return render.Value{}, fmt.Errorf("the parameter %s is one of: %s", name, strings.Join(periods, ", "))
	}
	return render.Value{}, fmt.Errorf("the parameter %s has no type this API reads", name)
}

// parseDate reads YYYY-MM-DD and nothing else. Formatting the day back and
// comparing is what refuses `2026-02-30`, which time.Parse would otherwise
// roll into March.
func parseDate(raw string) (dialect.Date, error) {
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil || t.Format(time.DateOnly) != raw {
		return dialect.Date{}, fmt.Errorf("not YYYY-MM-DD")
	}
	return dialect.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}, nil
}

// onlyRef reports whether a param is a bare `{ref: name}`.
func (p Param) onlyRef() bool {
	return p.Type == "" && p.MaxLen == 0 && p.Min == nil && p.Max == nil &&
		len(p.Values) == 0 && p.Default == nil && !p.Required
}
