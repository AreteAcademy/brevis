package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EVERY WAY OF REFUSING A REQUEST GOES THROUGH SOMETHING THAT COUNTS IT.
//
// This is the test issue #39 asked for, and the reason it reads the package's
// own source instead of exercising cases: a table of the four refusals that
// exist today covers three of five the moment a fifth is added. It would pass
// for ever and prove less every year.
//
// So the invariant is structural rather than behavioural — `http.Error` is
// called only where counting is guaranteed — and the allowlist below is the
// whole argument, which is why each entry carries its reason.
func TestEveryRefusalGoesThroughSomethingThatCounts(t *testing.T) {
	allowed := map[string]string{
		// The pipe's funnel. It counts and then refuses, in that order.
		"server.go": "inside (*pipe).refuse",
		// The guard, which calls back before refusing. It cannot use the
		// funnel: it runs before routing, so there is no pipe yet.
		"auth.go": "inside guard, after the refused callback",
		// NOT an ingest refusal. This is /metrics failing to render itself,
		// on another listener, and counting it in a counter it could not
		// render would be a joke at the operator's expense.
		"exposition.go": "the metrics endpoint failing, a different surface",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		n := strings.Count(string(src), "http.Error(")
		if n == 0 {
			continue
		}
		if _, ok := allowed[name]; !ok {
			offenders = append(offenders, name)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("these files refuse a request and are not on the allowlist: %v\n"+
			"A refusal written outside (*pipe).refuse is a refusal nobody counts. "+
			"Route it through the funnel, or add the file here WITH the reason it "+
			"does not need to be.", offenders)
	}

	// The allowlist must not outlive what it describes, either: a file that
	// stops refusing should leave, or the next reader believes it still does.
	for name := range allowed {
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Errorf("%s is on the allowlist and does not exist", name)
			continue
		}
		if !strings.Contains(string(src), "http.Error(") {
			t.Errorf("%s is on the allowlist and no longer refuses anything; remove it", name)
		}
	}
}

// The pipe refuses in exactly one place, so the funnel is the funnel.
func TestThePipeRefusesOnlyThroughRefuse(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "http.Error("); n != 1 {
		t.Errorf("server.go calls http.Error %d times, wanted 1 (the one inside "+
			"(*pipe).refuse). Another one is a refusal that counts nothing.", n)
	}
}
