// Package redis is the Redis backend for a gateway's metastore.
//
// Importing it costs the Redis client. A gateway with one replica does not
// need it -- `memory` is the design there -- and a binary that never imports
// this does not carry it.
//
// What it buys, and it is not durability: Redis is a cache here too, and
// losing it costs a round trip to the destination and nothing else. What it
// buys is COORDINATION. With several replicas, `memory` gives each its own,
// so a debounce debounces nothing and `naming.max_new_per_hour` bounds a
// process rather than a deployment.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/AreteAcademy/brevis/gateway"
)

// Name is what the YAML calls this backend.
const Name = gateway.MetastoreRedis

// Open connects and checks the connection before returning.
//
// Checked here, at startup: a gateway whose Redis address is wrong should fail
// to go ready rather than go ready and discover it on the first table -- where
// the failure is a batch in the dead letter instead of a pod that never
// reported healthy.
func Open(ctx context.Context, addr string) (gateway.Metastore, error) {
	opt, err := goredis.ParseURL(addr)
	if err != nil {
		// Not a URL: take it as host:port, which is what most deployments put
		// in an environment variable.
		opt = &goredis.Options{Addr: addr}
	}
	client := goredis.NewClient(opt)

	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(ping).Err(); err != nil {
		return nil, fmt.Errorf("the metastore's Redis did not answer: %w", err)
	}
	return &store{client: client}, nil
}

type store struct{ client *goredis.Client }

func (s *store) Describe() string { return Name }

func (s *store) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Put stores a value. A ttl of zero is passed through, and go-redis reads that
// as SET with no expiry -- which is what `ttl: 0` asks for. Of the three
// backends this was the only one that already meant it.
func (s *store) Put(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Claim is SET NX EX: set only if absent, with an expiry.
//
// One round trip and atomic, which is the whole reason this backend exists. It
// is a debounce and not a lock -- there is no release and no lease to renew, so
// a replica that dies holding one stalls nobody: the key expires and the next
// batch tries.
func (s *store) Claim(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, key, "1", ttl).Result()
}

// Incr sets the expiry only when the counter is CREATED.
//
// Extending it on every increment is the mistake that makes a busy key immortal
// and the window stop rolling -- the limit would then be a lifetime total
// rather than a rate.
func (s *store) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			return n, err
		}
	}
	return n, nil
}
