package memcached

import (
	"math"
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

// AND THE FOURTH CASE, WHICH IS 2038.
//
// An absolute expiry is an int32 because the protocol says so, and
// `int32(time.Now().Add(ttl).Unix())` SILENTLY WRAPS once that sum passes
// 2147483647 — 19 January 2038, or sooner with a long enough TTL. Wrapped,
// it is negative, and memcached reads a negative expiry as a moment long
// past: the item is discarded the instant it arrives.
//
// That is the same failure the 1970 case above exists for, reached by the
// other end of the number, and with the same symptom: a cache that silently
// never hits. Found by gosec (G115) the first time this repository pointed a
// scanner at the gateway.
//
// The answer is the furthest this protocol can say, not a wrapped number. A
// cache entry that lives until 2038 instead of 2046 is wrong in a way nobody
// notices; one that expires on arrival is wrong in a way nobody can explain.
func TestAnExpiryBeyondWhatTheProtocolCanSayIsNotWrapped(t *testing.T) {
	for _, ttl := range []time.Duration{
		20 * 365 * 24 * time.Hour,  // past 2038 from any plausible "now"
		100 * 365 * 24 * time.Hour, // and far past it
		1<<62 - 1,                  // and the largest Duration there is
	} {
		got := seconds(ttl)
		if got < 0 {
			t.Errorf("seconds(%v) = %d, which memcached reads as 1970: the "+
				"item would expire on arrival", ttl, got)
			continue
		}
		if got < int32(time.Now().Unix()) {
			t.Errorf("seconds(%v) = %d, which is already in the past", ttl, got)
		}
	}
	// AND IT SATURATES RATHER THAN APPROXIMATES: the furthest this protocol
	// can express is the honest answer to "keep this for a century".
	if got := seconds(100 * 365 * 24 * time.Hour); got != math.MaxInt32 {
		t.Errorf("seconds(a century) = %d, want %d", got, int32(math.MaxInt32))
	}
}
