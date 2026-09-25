package autotable

import (
	"sync"
	"time"
)

// DefaultTTL is how long the cache may be wrong.
//
// A table dropped by hand outside the gateway makes every cached entry a lie.
// Sixty seconds of wrongness is recoverable -- the next write recreates it --
// and an hour is an incident.
const DefaultTTL = 60 * time.Second

// metastore remembers what is known about a table, so a per-event write does
// not become a per-event GetTable.
//
// It is a CACHE and not a source of truth. The destination is the source of
// truth, and it settles the race that this only reduces: N replicas creating
// one table at the same moment is the normal case, not the edge, and
// AlreadyExists is success.
//
// Negative entries are cached too, and that is the half people leave out: "this
// table does not exist" is the answer that saves a round trip on the hot path
// of a new producer retrying.
type metastore struct {
	ttl time.Duration

	mu sync.Mutex
	by map[string]entry
}

type entry struct {
	exists bool
	until  time.Time
}

func newMetastore(ttl time.Duration) *metastore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &metastore{ttl: ttl, by: map[string]entry{}}
}

// get reports what is known, and whether anything is known at all.
func (m *metastore) get(table string, now time.Time) (exists, known bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.by[table]
	if !ok || now.After(e.until) {
		return false, false
	}
	return e.exists, true
}

func (m *metastore) put(table string, exists bool, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.by[table] = entry{exists: exists, until: now.Add(m.ttl)}
}
