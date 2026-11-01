package auth

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Outcome is what one throttled login attempt came to.
type Outcome int

const (
	Signed  Outcome = iota // the credential matched
	Refused                // it did not
	Locked                 // this source failed too often; the hash was not run
	Busy                   // every hash slot was taken; the hash was not run
)

const (
	// hashSlots caps the password hashes running at once. One costs ~0.2 s of
	// CPU (PBKDF2), so without a cap a few dozen concurrent POSTs to /login
	// starve /health and Kubernetes restarts the pod.
	hashSlots = 2
	// freeFailures are forgiven before a source is locked: typos happen.
	freeFailures = 3
	baseLock     = 2 * time.Second
	maxLock      = 15 * time.Minute
	// SharedKeyMaxLock caps the lockout when every client may share one key
	// (no trusted client-IP header): a guesser must not be able to keep the
	// operator out of the console for 15 minutes at a time.
	SharedKeyMaxLock = 30 * time.Second
	// forget is how long a source's failures are remembered.
	forget     = 15 * time.Minute
	maxSources = 10000
)

// slotWait is how long an attempt waits for a hash slot before it is Busy:
// long enough that a burst from a few sources is served in turn, short enough
// that a flood is not queued forever. A variable so a test can shorten it.
var slotWait = 2 * time.Second

type failure struct {
	count int
	last  time.Time
	until time.Time // locked until
}

// Limiter bounds login attempts: a few hashes at a time, one per source, and
// an exponential lockout per source after repeated failures. In memory and per
// process; shortcut: with several replicas each keeps its own counters, so the
// effective limit is multiplied by the replica count -- move to a shared
// store if Brevis ever runs more than a couple.
type Limiter struct {
	mu       sync.Mutex
	slots    chan struct{}
	fails    map[string]*failure
	inflight map[string]bool
	now      func() time.Time // injectable for tests
	// MaxLock caps one lockout; zero is 15 minutes.
	MaxLock time.Duration
}

func NewLimiter() *Limiter {
	return &Limiter{
		slots:    make(chan struct{}, hashSlots),
		fails:    map[string]*failure{},
		inflight: map[string]bool{},
		now:      time.Now,
	}
}

// ClientKey is the key an attempt is counted under: the address of the client,
// IPv6 reduced to its /64 (one subscriber holds a whole /64, so rotating inside
// it must not reset the counters). With header set (BREVIS_AUTH_CLIENT_IP_HEADER,
// for a deployment behind a proxy we trust) it is the LAST entry of that
// header -- the one our proxy appended; the first is whatever the client sent.
// Absent or invalid, it falls back to the connection's own address.
func ClientKey(r *http.Request, header string) string {
	if header != "" {
		if v := strings.Join(r.Header.Values(header), ","); v != "" {
			if key, ok := ipKey(v[strings.LastIndex(v, ",")+1:]); ok {
				return key
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if key, ok := ipKey(host); ok {
		return key
	}
	return host
}

func ipKey(s string) (string, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String(), true
	}
	return ip.String(), true
}

// Do runs check (the expensive hash) for source unless the source is locked,
// already has a hash running, or no slot frees up within slotWait, and records
// the result. retry is how long a Locked source must wait.
func (l *Limiter) Do(ctx context.Context, source string, check func() bool) (out Outcome, retry time.Duration) {
	l.mu.Lock()
	now := l.now()
	f := l.fails[source]
	if f != nil && !now.Before(f.until) && now.Sub(f.last) > forget {
		delete(l.fails, source) // failures decay with time
		f = nil
	}
	if f != nil && now.Before(f.until) {
		l.mu.Unlock()
		return Locked, f.until.Sub(now)
	}
	if f == nil && len(l.fails) >= maxSources {
		l.evict(now)
		if len(l.fails) >= maxSources { // every entry is a live lock: keep them
			l.mu.Unlock()
			return Busy, time.Second
		}
	}
	// One hash per source: a few addresses cannot hold every global slot.
	if l.inflight[source] {
		l.mu.Unlock()
		return Busy, time.Second
	}
	l.inflight[source] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.inflight, source); l.mu.Unlock() }()

	t := time.NewTimer(slotWait)
	defer t.Stop()
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	case <-t.C:
		return Busy, time.Second
	case <-ctx.Done():
		return Busy, time.Second
	}

	ok := check()

	l.mu.Lock()
	defer l.mu.Unlock()
	if ok {
		delete(l.fails, source)
		return Signed, 0
	}
	now = l.now()
	f = l.fails[source]
	if f == nil {
		if len(l.fails) >= maxSources {
			l.evict(now)
		}
		if len(l.fails) >= maxSources {
			return Refused, 0 // not recorded: a live lock is never dropped for it
		}
		f = &failure{}
		l.fails[source] = f
	}
	f.count++
	f.last = now
	if f.count > freeFailures {
		d := baseLock << min(f.count-freeFailures-1, 20)
		ceiling := l.MaxLock
		if ceiling == 0 {
			ceiling = maxLock
		}
		f.until = now.Add(min(d, ceiling))
	}
	return Refused, 0
}

// evict makes room without ever dropping a source that is still locked: the
// forgotten ones go first, then unlocked ones, arbitrary.
func (l *Limiter) evict(now time.Time) {
	for k, f := range l.fails {
		if !now.Before(f.until) && now.Sub(f.last) > forget {
			delete(l.fails, k)
		}
	}
	for k, f := range l.fails {
		if len(l.fails) < maxSources {
			return
		}
		if !now.Before(f.until) {
			delete(l.fails, k)
		}
	}
}
