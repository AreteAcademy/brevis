package catalog

import "reflect"

// ChangeKind is what happened to one setting between two configurations.
type ChangeKind string

const (
	Changed ChangeKind = "changed"
	Added   ChangeKind = "added"
	Removed ChangeKind = "removed"

	// NowWritten and NowDefault are the same value, decided by somebody else:
	// written into the file where it used to be the default, or dropped from
	// the file and filled in with what it already was. What runs did not
	// change; who chose it did, and the next default to move would now move
	// it -- or no longer would.
	NowWritten ChangeKind = "now_written"
	NowDefault ChangeKind = "now_default"
)

// Change is one setting's difference between two configurations.
type Change struct {
	Path          string
	Kind          ChangeKind
	Before, After any
}

// Diff is what changed from one configuration to the next, setting by
// setting: in the newer configuration's order, then what it no longer has, in
// the order the older one had it. Equal configurations have no changes.
func Diff(before, after []ManifestField) []Change {
	old := make(map[string]ManifestField, len(before))
	for _, f := range before {
		old[f.Path] = f
	}
	var out []Change
	seen := make(map[string]bool, len(after))
	for _, f := range after {
		seen[f.Path] = true
		was, ok := old[f.Path]
		switch {
		case !ok:
			out = append(out, Change{Path: f.Path, Kind: Added, After: f.Value})
		case !reflect.DeepEqual(was.Value, f.Value):
			out = append(out, Change{Path: f.Path, Kind: Changed, Before: was.Value, After: f.Value})
		case was.Default && !f.Default:
			out = append(out, Change{Path: f.Path, Kind: NowWritten, Before: was.Value, After: f.Value})
		case !was.Default && f.Default:
			out = append(out, Change{Path: f.Path, Kind: NowDefault, Before: was.Value, After: f.Value})
		}
	}
	for _, f := range before {
		if !seen[f.Path] {
			out = append(out, Change{Path: f.Path, Kind: Removed, Before: f.Value})
		}
	}
	return out
}
