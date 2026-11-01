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
	var (
		mu   sync.Mutex
		n    int
		at   time.Time
		held bool
	)
	return func(ctx context.Context) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		if held && now().Sub(at) < ttl {
			return n, nil
		}
		v, err := read(ctx)
		if err != nil {
			return 0, err
		}
		n, at, held = v, now(), true
		return v, nil
	}
}
