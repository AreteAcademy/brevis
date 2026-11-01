package auth

import (
	"testing"
	"time"
)

func TestLimiterLocksAfterRepeatedFailuresAndReleasesAfterTheWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	calls := 0
	bad := func() bool { calls++; return false }

	for i := 0; i < freeFailures+1; i++ {
		if out, _ := l.Do("a", bad); out != Refused {
			t.Fatalf("attempt %d = %v; expected Refused", i, out)
		}
	}
	before := calls
	if out, retry := l.Do("a", bad); out != Locked || retry <= 0 {
		t.Fatalf("got %v, %v; expected Locked with a retry", out, retry)
	}
	if calls != before {
		t.Error("a locked source still ran the hash")
	}
	if out, _ := l.Do("b", bad); out != Refused {
		t.Errorf("another source = %v; expected it to be unaffected", out)
	}

	now = now.Add(baseLock + time.Millisecond)
	if out, _ := l.Do("a", func() bool { return true }); out != Signed {
		t.Errorf("after the window = %v; expected Signed", out)
	}
	// A success forgets the source.
	if out, _ := l.Do("a", bad); out != Refused {
		t.Errorf("after a success = %v; expected a fresh count", out)
	}
}

func TestLimiterBacksOffExponentiallyAndStaysBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	bad := func() bool { return false }
	var last time.Duration
	for i := 1; i <= freeFailures+4; i++ {
		l.Do("a", bad)
		lock := l.fails["a"].until.Sub(now)
		if i > freeFailures+1 && lock != 2*last {
			t.Errorf("failure %d: lock %v; expected double %v", i, lock, last)
		}
		last = lock
		now = now.Add(maxLock) // skip the lock, keep the count
	}
	for i := 0; i < maxSources+50; i++ {
		l.Do(string(rune(i+1))+"x", bad)
	}
	if len(l.fails) > maxSources {
		t.Errorf("%d entries; the cap is %d", len(l.fails), maxSources)
	}
}

func TestLimiterRefusesBeyondTheSlotsWithoutQueueing(t *testing.T) {
	l := NewLimiter()
	hold, release := make(chan struct{}), make(chan struct{})
	for i := 0; i < hashSlots; i++ {
		go l.Do("x", func() bool { hold <- struct{}{}; <-release; return false })
		<-hold
	}
	ran := false
	if out, _ := l.Do("y", func() bool { ran = true; return true }); out != Busy || ran {
		t.Errorf("got %v, ran=%v; expected Busy without running the hash", out, ran)
	}
	close(release)
}
