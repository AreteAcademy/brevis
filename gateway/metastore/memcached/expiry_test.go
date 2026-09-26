package memcached

import (
	"testing"
	"time"
)

// How this protocol reads an expiry, and the two rules that bite.
//
// Pure, because both are arithmetic and neither needs a server -- and because
// the second one is invisible against a server: an item stored with an expiry
// memcached read as a 1970 timestamp is simply never there, and the symptom is
// a cache that silently never hits.
func TestSecondsRendersTheThreeCases(t *testing.T) {
	// `ttl: 0` means never, and memcached spells never as 0. The floor this
	// used to have turned that into one second -- "keep this forever" became
	// "forget it in a second".
	if got := seconds(0); got != 0 {
		t.Errorf("seconds(0) = %d; zero has to survive as never", got)
	}

	// Under a second is still a second: expiry here is second-granular, which
	// a flaky counter test learned the hard way.
	if got := seconds(400 * time.Millisecond); got != 1 {
		t.Errorf("seconds(400ms) = %d, want 1", got)
	}
	if got := seconds(90 * time.Second); got != 90 {
		t.Errorf("seconds(90s) = %d", got)
	}

	// Thirty days is where memcached stops reading seconds and starts reading
	// a Unix timestamp. Sent as a relative number, 31 days is a moment in
	// January 1970 and the item expires the instant it is stored.
	long := seconds(31 * 24 * time.Hour)
	if long < int32(time.Now().Unix()) {
		t.Errorf("seconds(31d) = %d, which memcached reads as a time in the "+
			"past: the item would expire on arrival", long)
	}
	// And it is the right instant, give or take the second this test took.
	want := int32(time.Now().Add(31 * 24 * time.Hour).Unix())
	if long < want-5 || long > want+5 {
		t.Errorf("seconds(31d) = %d, want about %d", long, want)
	}

	// The boundary itself is still relative.
	if got := seconds(memcachedRelativeMax); got != int32(memcachedRelativeMax.Seconds()) {
		t.Errorf("exactly 30 days = %d, want it still relative", got)
	}
}
