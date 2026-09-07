package runcontext_test

import (
	"encoding/json"
	"strings"
	"testing"

	rc "github.com/AreteAcademy/brevis/internal/domain/runcontext"
)

// The pipeline the request described: extract, transform, load.
var pipeline = map[string][]string{
	"transform": {"extract"},
	"load":      {"transform"},
}

// A step reads what it depends on, transitively.
//
// Transitive is the case that matters: `load` depends on `transform` depends on
// `extract`, and `load` legitimately wants the bucket `extract` published.
// Direct-only would make every step copy its input forward at each hop.
func TestAStepSeesItsDependenciesTransitively(t *testing.T) {
	for _, c := range []struct {
		node string
		want []string
	}{
		{"extract", nil},
		{"transform", []string{"extract"}},
		{"load", []string{"extract", "transform"}},
	} {
		got := rc.Visible(pipeline, c.node)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("Visible(%q) = %v, want %v", c.node, got, c.want)
		}
	}
}

// Isolation, which is the property the whole design is for.
//
// A step cannot read a step it does not depend on -- and that is not a policy
// enforced somewhere, it is what Assemble builds. `gold_metrics` and
// `gold_users` run in parallel off the same parent and neither can see the
// other, so there is no ordering to get wrong.
func TestParallelStepsCannotSeeEachOther(t *testing.T) {
	diamond := map[string][]string{
		"metrics": {"silver"},
		"users":   {"silver"},
		"publish": {"metrics", "users"},
	}
	published := map[string]json.RawMessage{
		"silver":  json.RawMessage(`{"rows":10}`),
		"metrics": json.RawMessage(`{"bucket":"m"}`),
		"users":   json.RawMessage(`{"bucket":"u"}`),
	}

	input, err := rc.Assemble(published, rc.Visible(diamond, "users"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input, `"metrics"`) {
		t.Errorf("users can see its sibling: %s", input)
	}
	if !strings.Contains(input, `"silver"`) {
		t.Errorf("users lost its parent: %s", input)
	}

	// And the step below both sees both, each under its own id -- so the two
	// `bucket` keys coexist instead of one overwriting the other.
	input, err = rc.Assemble(published, rc.Visible(diamond, "publish"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal([]byte(input), &got); err != nil {
		t.Fatal(err)
	}
	if got["metrics"]["bucket"] != "m" || got["users"]["bucket"] != "u" {
		t.Errorf("the two buckets did not survive side by side: %s", input)
	}
}

// A step in the visible set that published nothing is simply absent.
func TestAStepThatPublishedNothingIsAbsentRatherThanEmpty(t *testing.T) {
	input, err := rc.Assemble(map[string]json.RawMessage{}, []string{"extract"})
	if err != nil {
		t.Fatal(err)
	}
	if input != "" {
		t.Errorf("input = %q, want empty -- an env var holding {} teaches a "+
			"consumer that the step ran and published an empty object", input)
	}
}

// Truncation is the failure this whole function exists for.
func TestParseTellsEmptyFromTruncated(t *testing.T) {
	t.Run("empty is legitimate", func(t *testing.T) {
		got, err := rc.Parse([]byte("  \n "))
		if err != nil || got != nil {
			t.Errorf("got %q, %v; want nil, nil -- most steps publish nothing", got, err)
		}
	})

	t.Run("an object passes through", func(t *testing.T) {
		got, err := rc.Parse([]byte(`{"rows": 48213}`))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"rows": 48213}` {
			t.Errorf("got %q", got)
		}
	})

	t.Run("cut mid-string is an error naming truncation", func(t *testing.T) {
		// What the platform actually hands back: a JSON object cut at the
		// ceiling. A naive read calls this "published nothing" and the next
		// step gets a missing key with no error anywhere.
		cut := `{"bucket":"s3://landing/2026-09-07/very-long-prefix` +
			strings.Repeat("x", MaxOver)
		_, err := rc.Parse([]byte(cut))
		if err == nil {
			t.Fatal("a truncated object was read as valid")
		}
		if !strings.Contains(err.Error(), "truncat") {
			t.Errorf("the error does not say truncation, which is the cause nine "+
				"times out of ten: %v", err)
		}
	})

	t.Run("a JSON value that is not an object is refused", func(t *testing.T) {
		for _, bad := range []string{`["a"]`, `42`, `"text"`, `null`} {
			if _, err := rc.Parse([]byte(bad)); err == nil {
				t.Errorf("%s was accepted; it is keyed by step id and has nowhere to go", bad)
			}
		}
	})
}

// MaxOver pushes a fixture past the ceiling.
const MaxOver = rc.MaxBytes

// The two causes of a missing key need different fixes, so they get different
// messages.
func TestMissingSaysWhichOfTheTwoCausesItIs(t *testing.T) {
	visible := []string{"extract", "transform"}

	t.Run("visible, but did not publish that key", func(t *testing.T) {
		err := rc.Missing("bucket", "extract", visible)
		if !strings.Contains(err.Error(), "did not publish") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("not visible, so depends_on is the fix", func(t *testing.T) {
		err := rc.Missing("bucket", "load", visible)
		if !strings.Contains(err.Error(), "depends_on") {
			t.Errorf("the message does not point at the fix: %v", err)
		}
		if !strings.Contains(err.Error(), "extract, transform") {
			t.Errorf("the message does not list what IS visible: %v", err)
		}
	})

	t.Run("nothing is visible at all", func(t *testing.T) {
		err := rc.Missing("bucket", "extract", nil)
		if !strings.Contains(err.Error(), "no context is visible") {
			t.Errorf("got %v", err)
		}
	})
}

// TestAPrefixedStepIsNotSplitOnTheFirstDot.
//
// `uses:` prefixes a child's step ids at publish, so `mlops.train` is ONE step
// and `mlops.train.improved` is its key `improved`. Splitting on the first dot
// reads that as the step `mlops`, which does not exist -- and the message would
// have blamed a step nobody wrote.
func TestAPrefixedStepIsNotSplitOnTheFirstDot(t *testing.T) {
	visible := []string{"prepare", "mlops.train"}

	step, key, ok := rc.SplitKey("mlops.train.improved", visible)
	if !ok || step != "mlops.train" || key != "improved" {
		t.Errorf("split = (%q, %q, %v)", step, key, ok)
	}
	// And an unprefixed one still works.
	if step, key, ok := rc.SplitKey("prepare.rows", visible); !ok || step != "prepare" || key != "rows" {
		t.Errorf("split = (%q, %q, %v)", step, key, ok)
	}
}

// The longest match wins, which is what keeps a prefix from swallowing the step
// it is a prefix of.
func TestTheLongestStepNameWins(t *testing.T) {
	visible := []string{"a", "a.b"}
	if step, key, ok := rc.SplitKey("a.b.c", visible); !ok || step != "a.b" || key != "c" {
		t.Errorf("split = (%q, %q, %v)", step, key, ok)
	}
}

// Nothing matching still returns a split, because the caller's next act is an
// error message and naming something beats naming nothing.
func TestNoMatchStillNamesSomething(t *testing.T) {
	step, key, ok := rc.SplitKey("ghost.rows", []string{"prepare"})
	if ok {
		t.Error("a step nobody declared matched")
	}
	if step != "ghost" || key != "rows" {
		t.Errorf("the fallback split is (%q, %q)", step, key)
	}
}
