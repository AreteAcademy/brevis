package gateway

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metastore is the shared state a gateway keeps ABOUT tables, not in them.
//
// Four primitives, and the set is small on purpose: it is the intersection of
// what memory, Redis and memcached can all do atomically. Anything richer --
// a rolling-window counter, a lock with a lease -- would work on one of them
// and be emulated badly on the others.
//
// It is a CACHE and a COORDINATOR, never a source of truth:
//
//   - Get and Put hold what is known about a table, and may be wrong. The
//     destination settles whether a table exists; N replicas racing to create
//     one is the normal case and `AlreadyExists` is success.
//   - Claim is what makes several replicas behave like one. Ten of them
//     detecting the same new field would be ten ALTERs against BigQuery's five
//     metadata operations per table per ten seconds -- a quota gone and a retry
//     storm. One Claim per field per window makes it one.
//   - Incr is the rolling-window counter behind `naming.max_new_per_hour`,
//     which without it bounds a PROCESS and not a deployment.
//
// Claim is a DEBOUNCE and not a lock, deliberately. A lock needs a lease, a
// lease needs a timeout, and a timeout too short splits the brain while too
// long stalls every replica behind one dead process. Losing a Claim costs a
// retry and nothing else, which is what Delta Lake and Iceberg do with
// optimistic commits and what Kafka Connect gets free from partition ordering.
type Metastore interface {
	// Get returns what was stored, and whether anything was.
	Get(ctx context.Context, key string) (string, bool, error)

	// Put stores a value for ttl. A ttl of ZERO means the entry does not
	// expire, and every backend has to mean it -- see MetastoreConfig.TTL.
	//
	// Only Put takes a zero that way. Claim and Incr are always given a
	// positive window by their callers and must never be handed zero: a claim
	// that does not expire is a lock, which this design refuses by name, and a
	// counter that does not expire is a rolling window that never rolls.
	Put(ctx context.Context, key, value string, ttl time.Duration) error

	// Claim stores a marker only if the key is absent, and reports whether
	// this caller is the one who stored it. Set-if-absent with an expiry: if
	// the holder dies, the key expires and somebody else tries.
	Claim(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// Incr adds one to a counter that expires, and returns the new value. The
	// expiry is set when the counter is created and not extended, which is
	// what makes a fixed window rather than one that never ends.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)

	// Describe names the backend, for a log and for an error.
	Describe() string
}

// MetastoreFunc opens a backend. `addr` is whatever the deployment put in the
// environment variable the config named -- never the config file, because an
// address carries a password often enough.
type MetastoreFunc func(ctx context.Context, addr string) (Metastore, error)

// Metastores is the backends this binary carries.
//
// A registry for the same reason the sinks have one: a gateway that writes to
// a local table should not carry a Redis client, and a binary that did not
// compile one in should say so by name at startup rather than on the first
// table.
type Metastores struct {
	mu sync.RWMutex
	by map[string]MetastoreFunc
}

func NewMetastores() *Metastores { return &Metastores{by: map[string]MetastoreFunc{}} }

// Register adds one. A duplicate is refused rather than overwritten.
func (m *Metastores) Register(name string, fn MetastoreFunc) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a metastore needs a type name")
	}
	if fn == nil {
		return fmt.Errorf("metastore %q is nil", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.by[name]; taken {
		return fmt.Errorf("a metastore called %q is already registered", name)
	}
	m.by[name] = fn
	return nil
}

// MustRegister is Register for a main with nowhere to return an error.
func (m *Metastores) MustRegister(name string, fn MetastoreFunc) {
	if err := m.Register(name, fn); err != nil {
		panic(err)
	}
}

// Open builds the backend a config asked for.
//
// `memory` is always available and needs no registration: it is the default,
// and a gateway that cannot start without Redis is a gateway with a new hard
// dependency for a cache.
func (m *Metastores) Open(ctx context.Context, cfg MetastoreConfig) (Metastore, error) {
	if cfg.Type == "" || cfg.Type == MetastoreMemory {
		return NewMemoryMetastore(), nil
	}

	var fn MetastoreFunc
	var ok bool
	if m != nil {
		m.mu.RLock()
		fn, ok = m.by[cfg.Type]
		m.mu.RUnlock()
	}
	if !ok {
		return nil, fmt.Errorf("metastore type %q is not one this binary carries "+
			"(it has: %s). They are compiled in, so this is a build that left it "+
			"out rather than a backend that does not exist", cfg.Type, m.names())
	}

	addr, _ := os.LookupEnv(cfg.AddrFrom)
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("`metastore.addr_from` named %q and it is empty, and "+
			"it is where %s's address was meant to be", cfg.AddrFrom, cfg.Type)
	}
	return fn(ctx, addr)
}

func (m *Metastores) names() string {
	out := []string{MetastoreMemory}
	if m != nil {
		m.mu.RLock()
		for n := range m.by {
			out = append(out, n)
		}
		m.mu.RUnlock()
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// NewMemoryMetastore is the default backend: this process, and nothing shared.
//
// It is the DESIGN and not a limitation for a single replica. What it cannot do
// is coordinate: with N replicas each has its own, so Claim never debounces
// anything and `max_new_per_hour` bounds a process. The docs say so, and
// `redis` exists for exactly that.
func NewMemoryMetastore() Metastore {
	return &memoryMetastore{by: map[string]memoryEntry{}}
}

type memoryMetastore struct {
	mu sync.Mutex
	by map[string]memoryEntry
}

type memoryEntry struct {
	value string
	count int64
	// until is when the entry stops being true. The ZERO TIME means never,
	// which is what `ttl: 0` asks for -- and `now.Add(0)` would have meant
	// "expired a nanosecond ago", so a zero TTL used to make every Get a miss
	// here while Redis read the same zero as "keep forever".
	until time.Time
}

// expiresAt turns a TTL into the instant an entry stops being true. Zero in,
// zero out, and Get reads the zero time as never.
func expiresAt(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func (m *memoryMetastore) Describe() string { return MetastoreMemory }

func (m *memoryMetastore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.by[key]
	if !ok || (!e.until.IsZero() && time.Now().After(e.until)) {
		return "", false, nil
	}
	return e.value, true, nil
}

func (m *memoryMetastore) Put(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.by[key] = memoryEntry{value: value, until: expiresAt(ttl)}
	return nil
}

func (m *memoryMetastore) Claim(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.by[key]; ok && time.Now().Before(e.until) {
		return false, nil
	}
	m.by[key] = memoryEntry{value: "1", until: time.Now().Add(ttl)}
	return true, nil
}

func (m *memoryMetastore) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.by[key]
	if !ok || time.Now().After(e.until) {
		// The expiry is set when the counter is CREATED and not extended on
		// every increment: otherwise a busy key never expires and the window
		// stops rolling.
		e = memoryEntry{until: time.Now().Add(ttl)}
	}
	e.count++
	m.by[key] = e
	return e.count, nil
}
