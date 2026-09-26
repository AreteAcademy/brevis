// Package memcached is the memcached backend for a gateway's metastore.
//
// Importing it costs almost nothing -- the client is a few hundred lines --
// and it exists for deployments that already run memcached and do not want a
// second thing to operate.
//
// It coordinates the same way Redis does and stores no less safely, because
// everything here is a cache: `Add` is atomic set-if-absent, which is the
// debounce, and `Increment` is the counter. What it does not have is Redis'
// richer types, and nothing here needs them.
package memcached

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"

	"github.com/AreteAcademy/brevis/gateway"
)

// Name is what the YAML calls this backend.
const Name = gateway.MetastoreMemcached

// Open connects and checks the connection before returning, for the reason the
// Redis backend gives: a wrong address should fail readiness, not the first
// table.
//
// A comma-separated address is several servers, which is how memcached is
// usually run -- the client hashes keys across them, so a Claim for one table
// always lands on the same server and stays atomic.
func Open(_ context.Context, addr string) (gateway.Metastore, error) {
	servers := strings.Split(addr, ",")
	for i := range servers {
		servers[i] = strings.TrimSpace(servers[i])
	}
	client := memcache.New(servers...)
	client.Timeout = 5 * time.Second
	if err := client.Ping(); err != nil {
		return nil, fmt.Errorf("the metastore's memcached did not answer: %w", err)
	}
	return &store{client: client}, nil
}

type store struct{ client *memcache.Client }

func (s *store) Describe() string { return Name }

func (s *store) Get(_ context.Context, key string) (string, bool, error) {
	item, err := s.client.Get(escape(key))
	if errors.Is(err, memcache.ErrCacheMiss) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(item.Value), true, nil
}

func (s *store) Put(_ context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(&memcache.Item{
		Key: escape(key), Value: []byte(value), Expiration: seconds(ttl),
	})
}

// Claim is Add, which memcached defines as "store only if the key is absent"
// and answers atomically. Same shape as Redis' SET NX.
func (s *store) Claim(_ context.Context, key string, ttl time.Duration) (bool, error) {
	err := s.client.Add(&memcache.Item{
		Key: escape(key), Value: []byte("1"), Expiration: seconds(ttl),
	})
	if errors.Is(err, memcache.ErrNotStored) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Incr needs the Add first: memcached's Increment fails on a missing key
// rather than starting at one. The Add is what sets the expiry, and it is set
// ONCE -- extending it per increment would make a busy key immortal and the
// window stop rolling.
func (s *store) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	k := escape(key)
	err := s.client.Add(&memcache.Item{Key: k, Value: []byte("1"), Expiration: seconds(ttl)})
	if err == nil {
		return 1, nil
	}
	if !errors.Is(err, memcache.ErrNotStored) {
		return 0, err
	}
	n, err := s.client.Increment(k, 1)
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}

// escape makes a key memcached will take: no spaces, no control characters,
// 250 bytes.
//
// The keys here are built from table and field names, which are already
// validated -- this is the belt for the day one of those rules loosens, because
// an invalid key is a protocol error that reads like a network failure.
func escape(key string) string {
	var b strings.Builder
	for _, r := range key {
		if r <= ' ' || r == 0x7f {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > 250 {
		return out[:250]
	}
	return out
}

// seconds is memcached's expiry. Anything over thirty days is read as an
// absolute unix time by the protocol, which is a trap nothing here goes near --
// the TTLs are seconds and minutes.
// memcachedRelativeMax is where memcached stops reading an expiry as a number
// of SECONDS and starts reading it as an absolute Unix timestamp.
//
// Thirty days. It is in the protocol and it is the kind of rule that bites
// once: a TTL of 31 days sent as 2,678,400 is read as a moment in January
// 1970, so the item expires the instant it is stored -- and the symptom is a
// cache that silently never hits.
const memcachedRelativeMax = 30 * 24 * time.Hour

// seconds renders a TTL the way this protocol reads one.
//
//	0            never expires. `ttl: 0` asks for exactly this, and the zero
//	             has to survive: flooring it to 1 would turn "keep this" into
//	             "forget it in a second", which is the opposite.
//	under 1s     one second, the smallest this protocol can express. Expiry
//	             here is second-granular, which a test learned the hard way.
//	over 30 days an absolute Unix timestamp, per the rule above.
func seconds(ttl time.Duration) int32 {
	if ttl <= 0 {
		return 0
	}
	if ttl > memcachedRelativeMax {
		return int32(time.Now().Add(ttl).Unix())
	}
	s := int32(ttl.Seconds())
	if s < 1 {
		return 1
	}
	return s
}
