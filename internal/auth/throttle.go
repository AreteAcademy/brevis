package auth

import (
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
	// forget is how long a source's failures are remembered.
	forget     = 15 * time.Minute
	maxSources = 10000
)

type failure struct {
	count int
	last  time.Time
	until time.Time // locked until
}

// Limiter bounds login attempts: a few hashes at a time, and an exponential
// lockout per source after repeated failures. In memory and per process;
// shortcut: with several replicas each keeps its own counters, so the
// effective limit is multiplied by the replica count -- move to a shared
// store if Brevis ever runs more than a couple.
type Limiter struct {
	mu    sync.Mutex
	slots chan struct{}
	fails map[string]*failure
	now   func() time.Time // injectable for tests
}

func NewLimiter() *Limiter {
	return &Limiter{
		slots: make(chan struct{}, hashSlots),
		fails: map[string]*failure{},
		now:   time.Now,
	}
}

// Do runs check (the expensive hash) for source unless the source is locked
// or every slot is busy, and records the result. It never queues: an excess
// attempt is answered at once. retry is how long a Locked source must wait.
func (l *Limiter) Do(source string, check func() bool) (out Outcome, retry time.Duration) {
	l.mu.Lock()
	now := l.now()
	if f := l.fails[source]; f != nil && now.Before(f.until) {
		l.mu.Unlock()
		return Locked, f.until.Sub(now)
	}
	l.mu.Unlock()

	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	default:
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
	f := l.fails[source]
	if f == nil {
		if len(l.fails) >= maxSources {
			l.evict(now)
		}
		f = &failure{}
		l.fails[source] = f
	}
	f.count++
	f.last = now
	if f.count > freeFailures {
		d := baseLock << min(f.count-freeFailures-1, 20)
		f.until = now.Add(min(d, maxLock))
	}
	return Refused, 0
}

// evict makes room: drops the forgotten sources, and if all are recent, an
// arbitrary one -- bounded memory beats an exact record under an address flood.
func (l *Limiter) evict(now time.Time) {
	for k, f := range l.fails {
		if now.Sub(f.last) > forget && !now.Before(f.until) {
			delete(l.fails, k)
		}
	}
	for k := range l.fails {
		if len(l.fails) < maxSources {
			return
		}
		delete(l.fails, k)
	}
}
