package generation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// prefix is every generation key's. `{brevis}` is a Redis Cluster hash tag:
// only what is between the braces is hashed, so every key this module
// writes is on one slot and an MGET across models is allowed.
const prefix = "brevis:gen:{brevis}:"

// Key is where a model's generation lives.
func Key(model string) string { return prefix + model }

// NewToken is a fresh generation: when it was written, and sixteen random
// hex digits.
//
// NOT A RUN ID, because a step is given none -- the engine hands a step its
// parameters and nothing that names the run. What a generation has to be is
// DIFFERENT after every rebuild, and this is; the time in front is for the
// human reading Redis, not for any comparison.
func NewToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(b)
}

// Write records that each model was rebuilt, as one generation.
//
// NO EXPIRY. A generation stays until the next build replaces it; one that
// expired would turn every cached answer over that model into a miss on a
// timer nobody set, which is the clock this whole design exists to avoid.
func Write(ctx context.Context, c *Client, models []string, token string) error {
	for _, m := range models {
		if err := c.Set(ctx, Key(m), []byte(token), 0); err != nil {
			return fmt.Errorf("the generation of %s could not be written: %w", m, err)
		}
	}
	return nil
}

// Read is each model's generation, in the order asked, in one round trip.
// A model with none is "": built by something that does not write one, or
// never built -- either way a stable part of a cache key.
func Read(ctx context.Context, c *Client, models []string) ([]string, error) {
	if len(models) == 0 {
		return nil, nil
	}
	keys := make([]string, len(models))
	for i, m := range models {
		keys[i] = Key(m)
	}
	vals, err := c.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out, nil
}
