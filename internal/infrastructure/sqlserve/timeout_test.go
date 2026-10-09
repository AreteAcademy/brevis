package sqlserve

import "testing"

// THE CONSOLE WAITS LONGER THAN THE SERVICE, which is CHECKPOINT B's F5.
//
// It waited ten seconds while `serve` ran for thirty, so a query taking
// eleven told the reader it could not be reached -- and the warehouse ran it
// to completion and billed for it anyway. The reader was told the opposite
// of what happened.
//
// Whoever refuses has to be the one who knows why: the service bounds the
// query and says so in a sentence, and this has to be here when it arrives.
func TestTheConsoleOutwaitsTheService(t *testing.T) {
	if timeout <= serveTimeout {
		t.Errorf("the console gives up after %s and the service runs for %s, "+
			"so its refusal never arrives", timeout, serveTimeout)
	}
}
