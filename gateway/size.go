package gateway

import (
	"fmt"
	"strconv"
	"strings"
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
