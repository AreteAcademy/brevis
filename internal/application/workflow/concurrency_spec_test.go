package workflow

import (
	"strings"
	"testing"
)

func parseConcurrency(t *testing.T, block string) (int, string, error) {
	t.Helper()
	y := "name: a\ntype: dag\n" + block + "steps:\n  - id: one\n    run: echo\n"
	w, err := Parse("a.yaml", []byte(y))
	return w.MaxActive, w.Group, err
}

// Every file written before groups existed reads exactly as it did.
func TestABareConcurrencyIsStillTheWorkflowsOwnLimit(t *testing.T) {
	limit, group, err := parseConcurrency(t, "concurrency: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if limit != 1 || group != "" {
		t.Errorf("limit %d, group %q; wanted 1 and no group", limit, group)
	}
}

// #32: the daily and the */15 that build the same dbt models name the same
// group, and their runs count against one limit.
func TestAGroupIsTheResourceTwoWorkflowsShare(t *testing.T) {
	limit, group, err := parseConcurrency(t, "concurrency:\n  group: jaffle_models\n  limit: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if limit != 2 || group != "jaffle_models" {
		t.Errorf("limit %d, group %q; wanted 2 and jaffle_models", limit, group)
	}
}

// Mutual exclusion is what a group is for, so it is what saying nothing means.
func TestAGroupWithoutALimitIsExclusive(t *testing.T) {
	limit, _, err := parseConcurrency(t, "concurrency: {group: jaffle_models}\n")
	if err != nil {
		t.Fatal(err)
	}
	if limit != 1 {
		t.Errorf("limit %d; a group with no limit has to mean 1", limit)
	}
}

func TestAGroupIsRefusedWhenItCannotHoldAnything(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		// No limit at all would make the group decorative: the runs would
		// overlap exactly as they did in #32, with a line in the YAML saying
		// they do not.
		{"unlimited", "concurrency: {group: g, limit: 0}\n", "limit"},
		{"bad name", "concurrency: {group: \"Jaffle Models\"}\n", "group"},
		{"negative", "concurrency: {group: g, limit: -1}\n", "negative"},
		// A typo inside the mapping is where KnownFields stops looking (see
		// TestACustomUnmarshalerStillHidesItsFields). Dropping `grop` would
		// leave a workflow with no group, overlapping its partner in silence --
		// the bug back, with nothing to show for it.
		{"unknown key", "concurrency: {grop: g, limit: 1}\n", "grop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseConcurrency(t, tc.block)
			if err == nil {
				t.Fatalf("%s was accepted", strings.TrimSpace(tc.block))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
