package catalog

import (
	"reflect"
	"testing"
)

func TestDiffNamesWhatChangedSettingBySetting(t *testing.T) {
	before := []ManifestField{
		{Path: "format", Value: "json"},
		{Path: "buffer.flush.every", Value: "1m"},
		{Path: "buffer.workers", Value: float64(4), Default: true},
		{Path: "sink.into.write", Value: "merge"},
		{Path: "sink.naming.allow", Value: []any{"app_"}},
	}
	after := []ManifestField{
		{Path: "format", Value: "json"},
		{Path: "buffer.flush.every", Value: "2m"},
		{Path: "buffer.flush.claim", Value: true},
		{Path: "buffer.workers", Value: float64(4)},
		{Path: "sink.naming.allow", Value: []any{"app_", "svc_"}},
	}
	want := []Change{
		{Path: "buffer.flush.every", Kind: Changed, Before: "1m", After: "2m"},
		{Path: "buffer.flush.claim", Kind: Added, After: true},
		{Path: "buffer.workers", Kind: NowWritten, Before: float64(4), After: float64(4)},
		{Path: "sink.naming.allow", Kind: Changed, Before: []any{"app_"}, After: []any{"app_", "svc_"}},
		{Path: "sink.into.write", Kind: Removed, Before: "merge"},
	}
	if got := Diff(before, after); !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got: %+v\nwant: %+v", got, want)
	}
}

// A value that stops being written and falls back to the same default is a
// change in who decided it, not in what runs -- and it is named as such.
func TestDiffNamesASettingThatWentBackToItsDefault(t *testing.T) {
	got := Diff(
		[]ManifestField{{Path: "retry.attempts", Value: float64(4)}},
		[]ManifestField{{Path: "retry.attempts", Value: float64(4), Default: true}})
	if len(got) != 1 || got[0].Kind != NowDefault {
		t.Fatalf("diff = %+v", got)
	}
}

func TestTheSameSettingsHaveNoDiff(t *testing.T) {
	f := []ManifestField{{Path: "format", Value: "json"}}
	if got := Diff(f, f); len(got) != 0 {
		t.Fatalf("diff = %+v", got)
	}
}
