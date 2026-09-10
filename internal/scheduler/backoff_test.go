package scheduler

import (
	"testing"
	"time"
)

// TestConfiguringNothingGivesAPolicyThatIsNotInert.
//
// The rule from CONTRIBUTING.md: a feature with a default gets a test that configures
// NOTHING. Two features shipped switched off in one week and both times every
// test set the field under test.
//
// Here it is sharper than usual, because the default is what the whole change
// is about. A consumer migrating 34 tasks that each asked for three attempts
// three minutes apart got three attempts inside three seconds -- and every
// test in this package passed, because every one of them set BackoffBase.
func TestConfiguringNothingGivesAPolicyThatIsNotInert(t *testing.T) {
	d := &Dispatcher{}
	d.cfg.defaults()

	if d.cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d", d.cfg.MaxAttempts)
	}

	// The attempts, at the times they actually land on. Written as the
	// CUMULATIVE clock rather than as the delays, because that is the thing
	// somebody wants to know and the thing that was wrong.
	var total time.Duration
	at := []time.Duration{0}
	for a := 1; a < d.cfg.MaxAttempts; a++ {
		total += d.backoff(a)
		at = append(at, total)
	}
	want := []time.Duration{0, 30 * time.Second, 90 * time.Second}
	for i := range want {
		if at[i] != want[i] {
			t.Errorf("attempt %d at %s, wanted %s (whole schedule: %v)", i+1, at[i], want[i], at)
		}
	}

	// And the property the number was chosen for: the LAST attempt has to land
	// outside a one-minute rate-limit window. Below that, a limiter that needs
	// a minute to reset sees every attempt inside it and rejects every one --
	// which is what "three attempts in three seconds" meant in practice.
	if last := at[len(at)-1]; last <= time.Minute {
		t.Errorf("the last attempt lands at %s, inside a one-minute window; "+
			"the retry is inert for the failure it exists for", last)
	}
	// The other side of the same choice: a definitive failure's alert is only
	// raised on the last attempt, so this number is also how long an alert is
	// delayed. Two minutes is the ceiling that was accepted.
	if last := at[len(at)-1]; last > 2*time.Minute {
		t.Errorf("a definitive failure is announced %s after it happened", last)
	}
}

// TestTheDelayDoublesAndThenStops.
func TestTheDelayDoublesAndThenStops(t *testing.T) {
	d := &Dispatcher{cfg: Config{BackoffBase: time.Minute, BackoffMax: 8 * time.Minute}}

	for _, c := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 8 * time.Minute}, // capped
		{9, 8 * time.Minute},
	} {
		if got := d.backoff(c.attempt); got != c.want {
			t.Errorf("attempt %d waits %s, wanted %s", c.attempt, got, c.want)
		}
	}
}

// TestTheCapExistsBecauseMaxAttemptsBecameAFlag.
//
// Without a ceiling the exponential is unbounded, and that became reachable the
// moment --max-attempts existed: these are values somebody types, not values
// somebody has to construct.
func TestTheCapExistsBecauseMaxAttemptsBecameAFlag(t *testing.T) {
	d := &Dispatcher{cfg: Config{MaxAttempts: 10, BackoffBase: time.Minute}}
	d.cfg.defaults() // fills BackoffMax, and only BackoffMax

	var total time.Duration
	for a := 1; a < d.cfg.MaxAttempts; a++ {
		total += d.backoff(a)
	}
	// Uncapped this is eight and a half hours, of which the last wait alone is
	// four.
	if total > 6*time.Hour {
		t.Errorf("ten attempts span %s", total)
	}
	if last := d.backoff(9); last != time.Hour {
		t.Errorf("the ninth wait is %s, and the cap is %s", last, d.cfg.BackoffMax)
	}
}

// TestAnAbsurdAttemptDoesNotRequeueInstantly.
//
// `base << (attempt-1)` overflows int64 and comes back NEGATIVE around attempt
// 35, and ZERO from 63 -- and a zero delay is an instant requeue, a hot loop
// against whatever was already failing. That is the worst possible behaviour
// for the code path whose whole job is to be gentle.
//
// The assertion is the exact value and not merely "positive": a wrapped
// multiplication can land on a plausible-looking small number, and a test that
// only checks the sign would call that a pass. Asserting BackoffMax is what
// makes this bite when the loop is replaced by a shift.
func TestAnAbsurdAttemptDoesNotRequeueInstantly(t *testing.T) {
	for _, base := range []time.Duration{time.Second, 30 * time.Second, 7 * time.Minute} {
		d := &Dispatcher{cfg: Config{BackoffBase: base, BackoffMax: time.Hour}}
		for _, attempt := range []int{33, 35, 41, 63, 64, 65, 1 << 20} {
			if got := d.backoff(attempt); got != time.Hour {
				t.Errorf("base %s, attempt %d: waits %s, wanted the cap", base, attempt, got)
			}
		}
		// The other end. A non-positive attempt cannot arrive -- it comes from
		// the database and starts at one -- and the answer if it ever did is
		// the FIRST delay, not the cap: waiting an hour because a number was
		// nonsense would turn a bookkeeping slip into an outage.
		for _, attempt := range []int{0, -1} {
			if got := d.backoff(attempt); got != base {
				t.Errorf("base %s, attempt %d: waits %s, wanted the first delay", base, attempt, got)
			}
		}
	}
}

// A base already above the cap is the cap, not an overflow waiting to happen.
func TestABaseAboveTheCapIsTheCap(t *testing.T) {
	d := &Dispatcher{cfg: Config{BackoffBase: 2 * time.Hour, BackoffMax: time.Hour}}
	for _, attempt := range []int{1, 2, 9} {
		if got := d.backoff(attempt); got != time.Hour {
			t.Errorf("attempt %d waits %s", attempt, got)
		}
	}
}

// TestDefaultsIsWhatAZeroConfigGets.
//
// cmd/brevis prints these numbers in `scheduler --help`. If Defaults() ever
// stopped agreeing with defaults(), the help would describe a policy the
// dispatcher does not have -- which is the same class as the node type the API
// and the island disagreed about, and it shipped for two weeks.
func TestDefaultsIsWhatAZeroConfigGets(t *testing.T) {
	var zero Config
	zero.defaults()
	if Defaults() != zero {
		t.Errorf("Defaults() = %+v, a zeroed Config gets %+v", Defaults(), zero)
	}
}
