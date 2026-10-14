package load

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
)

// THE GOOGLE CLIENTS ARE BUILT ONCE PER PROCESS, and that is #87.
//
// New used to build a BigQuery client and a storage client on every call, and
// every caller made one call per write: Table.Write per batch, Table.Job per
// reconcile, Table.Check per run. Each client brought an HTTP transport of its
// own, a TLS handshake inside the caller's deadline, and idle connections that
// Close does not end -- measured, 50 calls left 150 goroutines whether the
// clients were closed or not. A gateway writing 94 tables a minute was minting
// 188 transports a minute.
//
// So one BigQuery client per project and emulator, and one storage client per
// emulator, made on first use and kept for the life of the process -- the way a
// *sql.DB is. The Google clients are safe for concurrent use. Nothing closes
// them: there is no point in a process's life where a loader knows it is the
// last.
var (
	clientsMu  sync.Mutex
	bqClients  = map[string]*bigquery.Client{}
	gcsClients = map[string]*storage.Client{}

	// The constructors, as variables so a test can make one fail.
	newBigQuery = bigquery.NewClient
	newStorage  = storage.NewClient

	// How many of each were built, for tests in this package.
	bqMade, gcsMade atomic.Int64
)

// sharedBigQuery returns the process's BigQuery client for a project.
func sharedBigQuery(ctx context.Context, project string) (*bigquery.Client, error) {
	key := project + "\x00" + emulatorBase()
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := bqClients[key]; ok {
		return c, nil
	}
	c, err := newBigQuery(detached(ctx), project, emulator(bigQueryPath)...)
	if err != nil {
		// Not remembered: credentials appear and networks come back, and
		// the next call should find out rather than replay this.
		return nil, fmt.Errorf("create bigquery client: %w", err)
	}
	bqMade.Add(1)
	bqClients[key] = c
	return c, nil
}

// sharedStorage returns the process's storage client, built the first time a
// load stages through GCS. A job lookup or an inline load never asks.
func sharedStorage(ctx context.Context) (*storage.Client, error) {
	key := emulatorBase()
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := gcsClients[key]; ok {
		return c, nil
	}
	c, err := newStorage(detached(ctx), emulator(storagePath)...)
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	gcsMade.Add(1)
	gcsClients[key] = c
	return c, nil
}

// detached is the context a client is built on: the caller's values, never its
// end. The client outlives the call, and the token source it builds keeps the
// context for every refresh -- built on a gateway's 60-second send window, a
// cached client would stop authenticating a minute later.
func detached(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// emulatorBase is the part of the key the emulator decides: a test pointed at
// one fake must never be handed the client built for another, or for Google.
func emulatorBase() string {
	return strings.TrimSpace(os.Getenv(EnvEmulator))
}
