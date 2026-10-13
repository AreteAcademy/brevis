package workflow_test

import (
	"strings"
	"testing"
)

func grouped(name, group, limit string) string {
	return "name: " + name + "\ntype: dag\nconcurrency: {group: " + group + ", limit: " + limit +
		"}\nsteps:\n  - id: one\n    run: echo\n"
}

// Two members of a group disagreeing on its limit is refused where the files
// are read, not left to the claim.
//
// The limit is a snapshot on each run and the claim reads it per item, so a
// group whose members said 1 and 3 would let whichever run the window
// function numbered first decide -- an accident, not a rule. Refused here,
// the person reading the two files can predict what runs.
func TestAGroupWhoseMembersDisagreeOnTheLimitIsRefused(t *testing.T) {
	_, err := resolve(t, grouped("daily", "jaffle_models", "1"), grouped("every_15", "jaffle_models", "3"))
	if err == nil {
		t.Fatal("a group declared with limits 1 and 3 was accepted")
	}
	for _, want := range []string{"jaffle_models", "daily (1)", "every_15 (3)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
}

func TestAGroupWhoseMembersAgreeIsAccepted(t *testing.T) {
	if _, err := resolve(t, grouped("daily", "jaffle_models", "1"), grouped("every_15", "jaffle_models", "1"),
		grouped("other", "warehouse", "3")); err != nil {
		t.Fatal(err)
	}
}
