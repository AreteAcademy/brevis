package gateway

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Size is a byte count written the way an operator writes one: 1MiB, 512KiB,
// 2GiB, or a plain number.
//
// A plain int would have worked, and `max_body: 1048576` is a number nobody
// reads twice and everybody miscounts once.
type Size int64

func (s *Size) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case int:
		*s = Size(v)
		return nil
	case int64:
		*s = Size(v)
		return nil
	case string:
		n, err := parseSize(v)
		if err != nil {
			return err
		}
		*s = n
		return nil
	}
	return fmt.Errorf("a size is a number or text like 1MiB, got %T", raw)
}

func parseSize(text string) (Size, error) {
	t := strings.TrimSpace(text)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
	} {
		if rest, ok := strings.CutSuffix(t, u.suffix); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil {
				return 0, fmt.Errorf("%q is not a size", text)
			}
			if n < 0 {
				return 0, fmt.Errorf("%q is negative", text)
			}
			return Size(n * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size (try 1MiB, 512KiB, or a number of bytes)", text)
	}
	return Size(n), nil
}

func (s Size) String() string {
	switch {
	case s >= 1<<30:
		return fmt.Sprintf("%.3gGiB", float64(s)/(1<<30))
	case s >= 1<<20:
		return fmt.Sprintf("%.3gMiB", float64(s)/(1<<20))
	case s >= 1<<10:
		return fmt.Sprintf("%.3gKiB", float64(s)/(1<<10))
	}
	return fmt.Sprintf("%dB", int64(s))
}

// Duration is a time.Duration that also accepts a bare `0`.
//
// YAML reads an unquoted number as an int and `time.Duration` wants a string,
// so `ttl: 0` -- the value the docs name for "never" -- was refused:
//
//	yaml: unmarshal errors:
//	  line 140: cannot unmarshal !!int `0` into time.Duration
//
// Which is a Go type name in front of somebody who wrote a config file, about
// the one value this field's documentation told them to write. Found by a
// consumer copying it out of the changelog.
//
// Zero is the ONE number where the unit adds nothing: zero seconds and zero
// hours are the same instant. Every other bare number is refused, and that is
// the other half of this type -- `ttl: 60` would be sixty NANOSECONDS under
// Go's own conversion, which nobody has ever meant. It says so rather than
// doing it.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("%q is not a duration (try 60s, 500ms, 2h)", v)
		}
		*d = Duration(parsed)
		return nil
	case int:
		if v == 0 {
			*d = 0
			return nil
		}
		return fmt.Errorf("%d has no unit. A bare number here would be %d "+
			"NANOSECONDS, which is never what anybody means -- write %ds, or "+
			"%dm. Only 0 may go without one, because zero seconds and zero "+
			"hours are the same instant", v, v, v, v)
	}
	return fmt.Errorf("a duration is text like 60s, or 0; got %T", raw)
}

func (d Duration) String() string { return time.Duration(d).String() }
