package mysql

import "testing"

// TestDeprecatedNamesStillResolve guards the aliases this package kept.
//
// ComParseTime and ParaJSON shipped in published versions. Deleting one would
// break a consumer's build with an error that names a symbol and explains
// nothing, so they stay until v1 -- and this test is what makes the deletion
// visible here instead of there.
func TestDeprecatedNamesStillResolve(t *testing.T) {
	if got, want := ComParseTime("u:p@tcp(h:3306)/d"), WithParseTime("u:p@tcp(h:3306)/d"); got != want {
		t.Fatalf("ComParseTime diverged: %q, want %q", got, want)
	}
	if got, want := ParaJSON([]byte("12.50"), "DECIMAL"), ToJSON([]byte("12.50"), "DECIMAL"); got != want {
		t.Fatalf("ParaJSON diverged: %v, want %v", got, want)
	}
}
