package catalog

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The engine judges targets from its own copy of the SDK's fixture, because it
// may not import the SDK (.github/scripts/engine-weight.sh). A copy that drifts
// is two validators disagreeing in silence -- a step's landing accepted by the
// library that wrote it and dropped by the engine that should keep it -- so the
// drift itself fails here.
func TestTheFixtureIsAnExactCopyOfTheSDKs(t *testing.T) {
	ours, err := os.ReadFile(filepath.Join("testdata", "targets.txt"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := os.ReadFile(filepath.Join("..", "..", "..", "sdk", "testdata", "targets.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ours, theirs) {
		t.Fatal("internal/domain/catalog/testdata/targets.txt differs from sdk/testdata/targets.txt; copy the SDK's over this one")
	}
}

func TestEveryFixtureTargetIsJudgedAsTheFixtureSays(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "targets.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	seen := 0
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		raw := sc.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		seen++
		body, reason, _ := strings.Cut(raw, " # ")
		verdict, target, _ := strings.Cut(body, " ")
		target = strings.TrimSpace(target)

		asLanding := ValidTarget(target, false)
		asPattern := ValidTarget(target, true)
		switch verdict {
		case "valid":
			if asLanding != nil || asPattern != nil {
				t.Errorf("line %d: %q refused: %v / %v", n, target, asLanding, asPattern)
			}
		case "pattern":
			if asLanding == nil {
				t.Errorf("line %d: %q accepted as a landing, want refused", n, target)
			}
			if asPattern != nil {
				t.Errorf("line %d: %q refused as a pattern: %v", n, target, asPattern)
			}
		case "invalid":
			if asLanding == nil || asPattern == nil {
				t.Errorf("line %d: %q accepted, want refused (%s)", n, target, reason)
			}
		default:
			t.Fatalf("line %d: unknown verdict %q", n, verdict)
		}
	}
	if seen == 0 {
		t.Fatal("the fixture has no cases")
	}
}
