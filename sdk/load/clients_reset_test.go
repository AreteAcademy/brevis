package load

import (
	"testing"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
)

// resetClients empties the cache for one test and restores it after.
func resetClients(t testing.TB) {
	t.Helper()
	clientsMu.Lock()
	savedBQ, savedGCS := bqClients, gcsClients
	bqClients, gcsClients = map[string]*bigquery.Client{}, map[string]*storage.Client{}
	clientsMu.Unlock()
	bqMade.Store(0)
	gcsMade.Store(0)
	t.Cleanup(func() {
		clientsMu.Lock()
		bqClients, gcsClients = savedBQ, savedGCS
		clientsMu.Unlock()
	})
}
