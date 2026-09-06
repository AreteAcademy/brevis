package postgres

import "testing"

// TestDeprecatedNamesStillResolve guards the aliases this package kept.
//
// ParaJSON and ParaJSONComOID shipped in published versions, so they stay
// until v1. Deleting one turns this test into a build failure, which is the
// point.
func TestDeprecatedNamesStillResolve(t *testing.T) {
	if got, want := ParaJSON("12.50"), ToJSON("12.50"); got != want {
		t.Fatalf("ParaJSON diverged: %v, want %v", got, want)
	}
	if got, want := ParaJSONComOID("x", oidDate), ToJSONWithOID("x", oidDate); got != want {
		t.Fatalf("ParaJSONComOID diverged: %v, want %v", got, want)
	}
}
