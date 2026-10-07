package agent_test

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/agent"
)

// An agent with no token runs ANY command for ANY caller.
//
// Not a weak password -- no password. Anything that can reach port 9443 in the
// namespace posts a command and this process runs it, as whatever user it is.
// It was allowed and warned about at startup, which is the shape of a
// dangerous choice made by OMITTING a flag: the warning scrolls past, and the
// manifest that caused it says nothing at all.
//
// So the omission is refused and the choice has to be written down. `ps` shows
// it, the manifest shows it, and a reviewer sees it.
func TestAnAgentWithNoTokenRefusesToStart(t *testing.T) {
	err := agent.Options{}.Check()
	if err == nil {
		t.Fatal("an agent with no token was accepted: anything that can reach " +
			"the port runs any command it sends")
	}
	for _, want := range []string{"--token-file", "--insecure-no-token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s:\n%v", want, err)
		}
	}
}

// And the open mode is still available, said out loud.
func TestTheOpenModeIsAvailableWhenItIsDeclared(t *testing.T) {
	if err := (agent.Options{InsecureNoToken: true}).Check(); err != nil {
		t.Fatalf("the declared open mode was refused: %v", err)
	}
}

// Declaring both is refused rather than resolved, because a reader cannot tell
// which one won.
func TestATokenAndTheOpenFlagTogetherAreRefused(t *testing.T) {
	err := agent.Options{Token: "s3cret", InsecureNoToken: true}.Check()
	if err == nil {
		t.Fatal("a token and --insecure-no-token together were accepted: one " +
			"of them is a lie and nothing says which")
	}
}

// A token on its own is the ordinary case.
func TestATokenIsEnough(t *testing.T) {
	if err := (agent.Options{Token: "s3cret"}).Check(); err != nil {
		t.Fatalf("an ordinary agent was refused: %v", err)
	}
}
