package remote

import (
	"slices"
	"testing"
	"time"
)

// Inside the package because the order is random by design, and what is
// tested here is where a refused address lands in it and for how long: the
// clock and the shuffle are fixed so the answer is one order, not a frequency.
func TestARefusedAddressIsTriedLastUntilTheSkipHasPassed(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	r := &Replicas{now: func() time.Time { return at }, shuffle: func([]string) {}}
	pool := []string{"10.0.0.1:9443", "10.0.0.2:9443", "10.0.0.3:9443"}

	r.drained("10.0.0.1:9443")

	at = at.Add(drainingSkip - time.Second)
	if got, want := r.order(pool), []string{"10.0.0.2:9443", "10.0.0.3:9443", "10.0.0.1:9443"}; !slices.Equal(got, want) {
		t.Errorf("within the skip: %v, wanted %v", got, want)
	}

	at = at.Add(time.Second)
	if got := r.order(pool); !slices.Equal(got, pool) {
		t.Errorf("once the skip has passed: %v, wanted %v", got, pool)
	}
}

// An order, never an exclusion: a pool where every replica refused is still
// dialed, because the alternative is a start that fails without asking anyone.
func TestEveryAddressRefusedIsStillTried(t *testing.T) {
	r := &Replicas{shuffle: func([]string) {}}
	pool := []string{"10.0.0.1:9443", "10.0.0.2:9443"}
	r.drained("10.0.0.1:9443")
	r.drained("10.0.0.2:9443")

	if got := r.order(pool); !slices.Equal(got, pool) {
		t.Errorf("%v, wanted every address, %v", got, pool)
	}
}
