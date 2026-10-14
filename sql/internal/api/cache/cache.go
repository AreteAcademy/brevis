// Package cache holds an endpoint's answers, compressed, until a model they
// read is rebuilt.
//
// INVALIDATED BY THE DATA, NOT BY THE CLOCK. Every key folds in the
// generation of each model the endpoint's SQL reads -- written by
// `brevis-sql build` when it rebuilds one -- so the first request after a
// rebuild is a miss and every one before it is a hit, however long that is.
// The ttl is a ceiling for the day a build forgets to write a generation,
// not the thing that refreshes an answer.
//
// A CACHE NEVER FAILS A REQUEST. Redis unreachable is a request answered
// from the warehouse and counted; the caller is owed an answer, not an
// opinion about the cache.
package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/generation"
)

// KV is the three commands this needs, which generation.Client has.
type KV interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	MGet(ctx context.Context, keys ...string) ([][]byte, error)
}

// Store is one API's cache.
type Store struct {
	kv       KV
	ttl      time.Duration
	maxEntry int
}

// New is a store over kv. ttl is every entry's ceiling; maxEntry is the
// most compressed bytes one answer may take.
func New(kv KV, ttl time.Duration, maxEntry int) *Store {
	return &Store{kv: kv, ttl: ttl, maxEntry: maxEntry}
}

// quality is Brotli's, from 0 to 11.
//
// FIVE, MEASURED on 2026-10-12 (Apple M-series, andybalholm/brotli v1.0.4)
// against a 200-row answer of the example's shape, 13 KB of JSON:
//
//	quality  1   5.2x   0.12 ms
//	quality  5   7.9x   0.24 ms
//	quality 11  10.8x  15.2  ms
//
// An answer is compressed once per generation and read many times, so the
// ratio matters -- but 11's sixty-fold cost lands on the request that
// missed, which is already the slow one, and 5 keeps it under a
// millisecond.
const quality = 5

// Key is where one answer lives.
//
// A HASH OF LENGTH-PREFIXED PARTS, so no two different inputs can
// concatenate alike: a tenant ending in what an endpoint begins with is a
// different pair, not the same string. The argument's Go type is in it, so
// the int 1 and the string "1" are two answers. The `{brevis}` hash tag
// keeps every key on one Redis Cluster slot, beside the generations.
//
// THE API AND ITS CONNECTION ARE IN IT. Found in the ship review: two APIs
// share one Redis by design -- the generations live there -- and staging
// and production serving an `orders` endpoint with the same SQL answered
// each other's rows. The API's name is in the prefix, so one API's entries
// can be flushed alone; the connection's identity is in the hash.
//
// AND SO IS THE SHAPE: which after hook made the answer, and in which build
// -- empty when none did. The same rows are a different answer through a
// different hook, or through the same name after a deploy changed it.
func Key(api, connection, endpoint, tenant, sql string, args []dialect.Arg, generations []string, shape string) string {
	h := sha256.New()
	part := func(s string) { _, _ = fmt.Fprintf(h, "%d:%s", len(s), s) }
	part(api)
	part(connection)
	part(endpoint)
	part(tenant)
	part(sql)
	for _, a := range args {
		part(a.Name)
		part(fmt.Sprintf("%T", a.Value))
		part(fmt.Sprint(a.Value))
	}
	part(shape)
	for _, g := range generations {
		part(g)
	}
	return "brevis:api:{brevis}:" + api + ":" + endpoint + ":" + hex.EncodeToString(h.Sum(nil))
}

// Generations is each model's current generation, in one round trip.
func (s *Store) Generations(ctx context.Context, models []string) ([]string, error) {
	if len(models) == 0 {
		return nil, nil
	}
	keys := make([]string, len(models))
	for i, m := range models {
		keys[i] = generation.Key(m)
	}
	vals, err := s.kv.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out, nil
}

// Get is the stored, COMPRESSED answer: a caller that accepts Brotli is
// sent these bytes as they are.
func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return s.kv.Get(ctx, key)
}

// Put compresses an answer and keeps it, unless it is over maxEntry.
//
// LARGER IS SERVED AND NOT KEPT. One huge answer evicting a thousand small
// ones is a cache working against itself.
func (s *Store) Put(ctx context.Context, key string, body []byte) (bool, error) {
	c, err := Compress(body)
	if err != nil {
		return false, err
	}
	if len(c) > s.maxEntry {
		return false, nil
	}
	return true, s.kv.Set(ctx, key, c, s.ttl)
}

// Compress is an answer as Brotli.
func Compress(body []byte) ([]byte, error) {
	var b bytes.Buffer
	w := brotli.NewWriterLevel(&b, quality)
	if _, err := w.Write(body); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// maxDecompressed bounds what one stored entry may expand to. A cache is
// shared state, and an entry somebody else wrote is not trusted to be the
// size its compressor promised.
const maxDecompressed = 256 << 20

// Decompress is a stored answer for a caller that does not accept Brotli.
func Decompress(c []byte) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(brotli.NewReader(bytes.NewReader(c)), maxDecompressed+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressed {
		return nil, fmt.Errorf("a cached answer expands past %d bytes", maxDecompressed)
	}
	return out, nil
}

// Group runs one function per key at a time, and hands its result to every
// caller that asked meanwhile -- the misses after a rebuild, collapsed.
//
// x/sync's singleflight, in thirty lines, for the reason the Redis client is
// in this module: a dependency is a conversation, and this is not one worth
// having.
type Group[T any] struct {
	mu    sync.Mutex
	calls map[string]*call[T]
}

type call[T any] struct {
	wg  sync.WaitGroup
	val T
	err error
	// dups is how many callers are waiting on this run -- x/sync's own
	// field, and what lets a test know every waiter has arrived.
	dups int
}

// Do runs fn for key, or waits for the run already going.
func (g *Group[T]) Do(key string, fn func() (T, error)) (T, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*call[T]{}
	}
	if c, running := g.calls[key]; running {
		c.dups++
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call[T]{}
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		c.wg.Done()
	}()
	c.val, c.err = fn()
	return c.val, c.err
}
