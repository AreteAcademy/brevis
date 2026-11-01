package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		if out, _ := l.Do(ctx, "a", bad); out != Refused {
			t.Fatalf("attempt %d = %v; expected Refused", i, out)
		}
	}
	before := calls
	if out, retry := l.Do(ctx, "a", bad); out != Locked || retry <= 0 {
		t.Fatalf("got %v, %v; expected Locked with a retry", out, retry)
	}
	if calls != before {
		t.Error("a locked source still ran the hash")
	}
	if out, _ := l.Do(ctx, "b", bad); out != Refused {
		t.Errorf("another source = %v; expected it to be unaffected", out)
	}

	now = now.Add(baseLock + time.Millisecond)
	if out, _ := l.Do(ctx, "a", func() bool { return true }); out != Signed {
		t.Errorf("after the window = %v; expected Signed", out)
	}
	// A success forgets the source.
	if out, _ := l.Do(ctx, "a", bad); out != Refused {
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
		l.Do(ctx, "a", bad)
		lock := l.fails["a"].until.Sub(now)
		if i > freeFailures+1 && lock != 2*last {
			t.Errorf("failure %d: lock %v; expected double %v", i, lock, last)
		}
		last = lock
		now = now.Add(maxLock) // skip the lock, keep the count
	}
	for i := 0; i < maxSources+50; i++ {
		l.Do(ctx, string(rune(i+1))+"x", bad)
	}
	if len(l.fails) > maxSources {
		t.Errorf("%d entries; the cap is %d", len(l.fails), maxSources)
	}
}

func TestLimiterRefusesBeyondTheSlotsWithoutQueueing(t *testing.T) {
	old := slotWait
	slotWait = 50 * time.Millisecond
	defer func() { slotWait = old }()
	l := NewLimiter()
	hold, release := make(chan struct{}), make(chan struct{})
	for i := 0; i < hashSlots; i++ {
		go l.Do(ctx, fmt.Sprint("x", i), func() bool { hold <- struct{}{}; <-release; return false })
		<-hold
	}
	ran := false
	if out, _ := l.Do(ctx, "y", func() bool { ran = true; return true }); out != Busy || ran {
		t.Errorf("got %v, ran=%v; expected Busy without running the hash", out, ran)
	}
	close(release)
}

var ctx = context.Background()

func TestClientKey(t *testing.T) {
	req := func(remote string, hdr ...string) *http.Request {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = remote
		for _, h := range hdr {
			r.Header.Add("X-Forwarded-For", h)
		}
		return r
	}
	for _, c := range []struct {
		name   string
		r      *http.Request
		header string
		want   string
	}{
		{"no header configured ignores the header", req("10.0.0.9:1", "6.6.6.6"), "", "10.0.0.9"},
		{"spoofed first entry, last wins", req("10.0.0.9:1", "6.6.6.6, 203.0.113.5"), "X-Forwarded-For", "203.0.113.5"},
		{"several header lines", req("10.0.0.9:1", "6.6.6.6", "203.0.113.5"), "X-Forwarded-For", "203.0.113.5"},
		{"invalid last falls back", req("10.0.0.9:1", "6.6.6.6, junk"), "X-Forwarded-For", "10.0.0.9"},
		{"absent falls back", req("10.0.0.9:1"), "X-Forwarded-For", "10.0.0.9"},
		{"ipv6 grouped by /64", req("[2001:db8:1:2:aaaa::1]:5"), "", "2001:db8:1:2::/64"},
		{"ipv6 same /64, other host", req("[2001:db8:1:2:bbbb::9]:5"), "", "2001:db8:1:2::/64"},
		{"ipv4-mapped is ipv4", req("[::ffff:192.0.2.1]:5"), "", "192.0.2.1"},
	} {
		if got := ClientKey(c.r, c.header); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLimiterNeverEvictsALockedSource(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < maxSources; i++ {
		l.fails[fmt.Sprint(i)] = &failure{count: 9, last: now, until: now.Add(time.Hour)}
	}
	ran := false
	if out, _ := l.Do(ctx, "new", func() bool { ran = true; return false }); out != Busy || ran {
		t.Errorf("got %v ran=%v; expected Busy when every entry is locked", out, ran)
	}
	if len(l.fails) != maxSources || l.fails["0"] == nil {
		t.Error("a locked entry was dropped")
	}
	// Once one lock lapses and is stale, there is room again.
	l.fails["0"].until = now.Add(-time.Second)
	l.fails["0"].last = now.Add(-2 * forget)
	if out, _ := l.Do(ctx, "new", func() bool { return false }); out != Refused {
		t.Errorf("got %v; expected Refused once there is room", out)
	}
}

func TestLimiterFailuresDecay(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < freeFailures; i++ {
		l.Do(ctx, "a", func() bool { return false })
	}
	now = now.Add(forget + time.Second)
	l.Do(ctx, "a", func() bool { return false })
	if c := l.fails["a"].count; c != 1 {
		t.Errorf("count = %d after the window; expected the old failures forgotten", c)
	}
}

func TestLimiterWaitsBrieflyForASlotThenIsBusy(t *testing.T) {
	old := slotWait
	slotWait = 200 * time.Millisecond
	defer func() { slotWait = old }()
	l := NewLimiter()
	hold, release := make(chan struct{}), make(chan struct{})
	for i := 0; i < hashSlots; i++ {
		go l.Do(ctx, fmt.Sprint("h", i), func() bool { hold <- struct{}{}; <-release; return false })
		<-hold
	}
	// Frees up inside the wait: served, not rejected.
	go func() { time.Sleep(50 * time.Millisecond); release <- struct{}{} }()
	if out, _ := l.Do(ctx, "waiter", func() bool { return true }); out != Signed {
		t.Errorf("waiter = %v; expected Signed after a slot freed", out)
	}
	// Nothing frees: Busy after about slotWait, hash not run.
	go l.Do(ctx, "refill", func() bool { hold <- struct{}{}; <-release; return false })
	<-hold
	start := time.Now()
	ran := false
	if out, _ := l.Do(ctx, "late", func() bool { ran = true; return true }); out != Busy || ran {
		t.Errorf("late = %v ran=%v; expected Busy", out, ran)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > time.Second {
		t.Errorf("waited %v; expected about %v", d, slotWait)
	}
	close(release)
}

func TestLimiterAllowsOneHashPerSource(t *testing.T) {
	l := NewLimiter()
	hold, release := make(chan struct{}), make(chan struct{})
	go l.Do(ctx, "same", func() bool { hold <- struct{}{}; <-release; return false })
	<-hold
	ran := false
	if out, _ := l.Do(ctx, "same", func() bool { ran = true; return true }); out != Busy || ran {
		t.Errorf("second hash from one source = %v ran=%v; expected Busy", out, ran)
	}
	close(release)
}
