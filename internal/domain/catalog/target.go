// Package catalog is what the engine knows about the destinations its steps
// and gateways write: their names, and nothing that would let it reach them.
package catalog

import (
	"fmt"
	"strings"
)

// ValidTarget reports whether s has the shape of a target.
//
// The engine CHECKS targets and never builds one. The SDK's Locate() is the
// only builder, and a second builder here would be the start of two names for
// one table. What is left to the engine is refusing what a step should never
// have sent: an unknown scheme, a user, a port, a query string -- the shape of
// a DSN pasted where a name belongs. Refusing drops the landing; it never
// repairs it, because a repaired target is an inferred one.
//
// allowPattern admits a trailing `/*`, which only a published gateway manifest
// may carry: auto_table creates tables per route, and the manifest can name the
// dataset they land in but not the tables themselves.
//
// This is a copy of the SDK's sdk/internal/core/target.go check, kept apart
// because ./cmd/brevis must not import the SDK. The two agree because they read
// the same cases: testdata/targets.txt here is a copy of the SDK's, and a test
// fails when it drifts.
func ValidTarget(s string, allowPattern bool) error {
	refuse := func(why string) error { return fmt.Errorf("target %q: %s", s, why) }

	if s == "" {
		return refuse("empty")
	}
	if len(s) > targetCeiling {
		return refuse(fmt.Sprintf("longer than %d bytes", targetCeiling))
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return refuse("no scheme")
	}
	_, table := tableSegments[scheme]
	if !table && !objectSchemes[scheme] {
		lower := strings.ToLower(scheme)
		if _, known := tableSegments[lower]; known || objectSchemes[lower] {
			return refuse("the scheme must be lower-case")
		}
		return refuse(fmt.Sprintf("unknown scheme %q", scheme))
	}
	for i := 0; i < len(rest); i++ {
		switch b := rest[i]; {
		case b <= ' ' || b == 0x7f:
			return refuse("contains whitespace or a control character")
		case b == '@':
			return refuse("contains '@', which only a DSN would")
		case b == '?':
			return refuse("contains a query string")
		case b == '#':
			return refuse("contains a fragment")
		}
	}
	if table {
		return validTable(scheme, rest, allowPattern, refuse)
	}
	return validObject(scheme, rest, refuse)
}

// Path segments per table scheme; object schemes take a bucket and a prefix.
var tableSegments = map[string]int{
	"bigquery": 3,
	"postgres": 3,
	"redshift": 3,
	"mysql":    2,
	"pubsub":   2,
}

var objectSchemes = map[string]bool{"s3": true, "gs": true, "file": true}

// targetCeiling bounds a target: it is a primary-key column and a cell on a
// screen, and nothing that names a table needs more.
const targetCeiling = 512

func validTable(scheme, rest string, allowPattern bool, refuse func(string) error) error {
	segs := strings.Split(rest, "/")
	if want := tableSegments[scheme]; len(segs) != want {
		return refuse(fmt.Sprintf("%s takes %d path segments, found %d", scheme, want, len(segs)))
	}
	last := len(segs) - 1
	for i, seg := range segs {
		if seg == "" {
			return refuse("an empty path segment")
		}
		// A domain-scoped BigQuery project is `example.com:project`; anywhere
		// else a colon is a port.
		if strings.Contains(seg, ":") && !(scheme == "bigquery" && i == 0) {
			return refuse("contains ':', a port")
		}
		if strings.Contains(seg, "*") && (seg != "*" || i != last || !allowPattern || scheme == "pubsub") {
			return refuse("a '*' is only valid as the whole last segment of a gateway pattern")
		}
	}
	return nil
}

func validObject(scheme, rest string, refuse func(string) error) error {
	if strings.ContainsAny(rest, ":*") {
		return refuse("an object target takes no ':' and no pattern")
	}
	if scheme == "file" {
		if !strings.HasPrefix(rest, "/") {
			return refuse("a file target is an absolute path")
		}
		rest = rest[1:]
		if rest == "" {
			return nil
		}
	}
	segs := strings.Split(rest, "/")
	if scheme != "file" && segs[0] == "" {
		return refuse("no bucket")
	}
	for i, seg := range segs {
		if seg == "" && i != len(segs)-1 {
			return refuse("an empty path segment")
		}
	}
	return nil
}
