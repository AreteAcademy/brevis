package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// A batch key names one delivery of one batch, across all its attempts.
//
// Issue #85: a retry that a destination cannot recognise as the same write is
// a second write. BigQuery names the load job after this key, so attempt two
// finds the job attempt one left running and takes its outcome, instead of
// starting another that writes the rows again.
//
// It travels in the context because the Sinker interface carries no options,
// and because auto_table hands each table's sink a context derived from the
// pipe's: every table of the batch sees the key, and the table name keeps
// their jobs apart.
type batchKeyType struct{}

// WithBatchKey returns ctx carrying key.
func WithBatchKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, batchKeyType{}, key)
}

// BatchKeyOf returns the key ctx carries, or "" when it carries none.
func BatchKeyOf(ctx context.Context) string {
	key, _ := ctx.Value(batchKeyType{}).(string)
	return key
}

// newBatchKey is random: two batches with identical events are still two
// deliveries.
func newBatchKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails; see crypto/rand.Read
	return hex.EncodeToString(b)
}
