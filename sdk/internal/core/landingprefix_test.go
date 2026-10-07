package core

import (
	"strings"
	"testing"
)

// The prefix a client prefers, out of whatever they typed.
//
// A PURE function and not a read of the environment, which is what lets the
// whole table exist. The same shape CreationPlan has, and for the same
// reason.
func TestNormalizeLandingPrefix(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		// Not configured, and the shape a compose file writes for "not
		// configured": `BREVIS_LANDING_PREFIX=` is a line people write
		// meaning nothing, and it must not be read as "I want none".
		{"", DefaultLandingPrefix},
		{"   ", DefaultLandingPrefix},

		// The three the client asked for, and they are the same answer.
		{"_NAME", "name_"},
		{"_NAME_", "name_"},
		{"NAME", "name_"},

		// Which means the rule is: trim `_` from BOTH ends, lower-case, and
		// append exactly one.
		{"NAME__", "name_"},
		{"__name__", "name_"},
		{"  name  ", "name_"},
		{"Acme_Bronze", "acme_bronze_"},

		// Idempotent on what it produces, and on today's default.
		{"brevis_", DefaultLandingPrefix},
		{"BREVIS_", DefaultLandingPrefix},
		{"name_", "name_"},
	} {
		t.Run("("+c.raw+")", func(t *testing.T) {
			got, err := NormalizeLandingPrefix(c.raw)
			if err != nil {
				t.Fatalf("%q: %v", c.raw, err)
			}
			if got != c.want {
				t.Errorf("%q → %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// What cannot be made into a prefix is refused, and never quietly defaulted.
//
// Falling back to `brevis_` would mean the operator asked for something and
// got the thing they were trying to replace — in the table names, where they
// would find out months later.
func TestNormalizeLandingPrefixRefusesWhatCannotBeOne(t *testing.T) {
	for _, c := range []struct{ raw, because string }{
		// Typed characters that leave nothing. NOT the empty case above:
		// somebody wrote underscores on purpose.
		{"_", "ingestion_id"},
		{"___", "ingestion_id"},

		// It would not be a legal column name. The test is on
		// prefix+"ingestion_id" and not on the prefix alone, which is what
		// catches a leading digit.
		{"9x", "column"},
		{"my name", "column"},
		{"a-b", "column"},
		{"café", "column"},

		// Long enough that the PREFIX is legal on its own and the COLUMN is
		// not. This is the case the whole-name check exists for, and the
		// only one: a leading digit is already caught by the bare name, so a
		// mutation validating just the prefix passed everything above.
		// ColumnName allows 128, and `<name>_ingestion_id` adds 13.
		{strings.Repeat("x", 120), "column"},
	} {
		t.Run("("+c.raw+")", func(t *testing.T) {
			got, err := NormalizeLandingPrefix(c.raw)
			if err == nil {
				t.Fatalf("%q was accepted as %q", c.raw, got)
			}
			if !strings.Contains(err.Error(), c.because) {
				t.Errorf("the refusal does not say why: %v", err)
			}
		})
	}

	// The empty case specifically must say what it collides with, because
	// "I want no prefix at all" is the first thing somebody tries.
	_, err := NormalizeLandingPrefix("_")
	if err == nil || !strings.Contains(err.Error(), "IngestionID") {
		t.Errorf("the refusal does not name the column it would collide "+
			"with: %v", err)
	}
}

// Unset means today's behaviour, and that is the only promise every existing
// table depends on.
func TestLandingPrefixDefaultsToBrevis(t *testing.T) {
	if LandingPrefix() != "brevis_" {
		t.Errorf("LandingPrefix() = %q with nothing configured", LandingPrefix())
	}
	if DefaultLandingPrefix != "brevis_" {
		t.Errorf("DefaultLandingPrefix = %q", DefaultLandingPrefix)
	}
}

// Set and unusable does not start.
//
// The alternative this repo already uses elsewhere — warn and carry on, as
// autoparams.go and runcontext.go do — is wrong here. Those degrade
// gracefully; this one would create tables named after the prefix the
// operator was trying to replace.
func TestAnUnusablePrefixPanics(t *testing.T) {
	t.Setenv(EnvLandingPrefix, "9x")

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("resolving an unusable prefix returned instead of panicking")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, EnvLandingPrefix) {
			t.Errorf("the panic does not name the variable: %v", r)
		}
		if !strings.Contains(msg, "9x") {
			t.Errorf("the panic does not show the value: %v", r)
		}
	}()
	_ = resolveLandingPrefix()
}

// And a usable one is taken.
func TestAConfiguredPrefixIsTaken(t *testing.T) {
	t.Setenv(EnvLandingPrefix, "_ACME_")
	if got := resolveLandingPrefix(); got != "acme_" {
		t.Errorf("resolveLandingPrefix() = %q, want acme_", got)
	}

	// Set to blank is the compose case, not a request.
	t.Setenv(EnvLandingPrefix, "")
	if got := resolveLandingPrefix(); got != DefaultLandingPrefix {
		t.Errorf("a blank variable gave %q, want the default", got)
	}
}
