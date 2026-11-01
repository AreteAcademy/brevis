package api

import (
	"context"
	"sync"
	"time"
)

// CachedInFlight keeps read's answer for ttl, as CachedFailing does for the
// failing list: the badge is drawn on every page and polled by every open
// tab, and a count a few seconds old says the same thing.
//
// An error is not kept: the next page asks again.
func CachedInFlight(read func(context.Context) (int, error), ttl time.Duration, now func() time.Time) func(context.Context) (int, error) {
	return cached(read, ttl, now)
}

// cached keeps read's answer for ttl. An error is not kept.
func cached[T any](read func(context.Context) (T, error), ttl time.Duration, now func() time.Time) func(context.Context) (T, error) {
	var (
		mu   sync.Mutex
		v    T
		at   time.Time
		held bool
	)
	return func(ctx context.Context) (T, error) {
		mu.Lock()
		defer mu.Unlock()
		if held && now().Sub(at) < ttl {
			return v, nil
		}
		got, err := read(ctx)
		if err != nil {
			var zero T
			return zero, err
		}
		v, at, held = got, now(), true
		return got, nil
	}
}
