package queue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Claim must take turns with every other claimer, in this process or another.
// The test plays the other claimer: it holds the claim lock, and Claim has to
// wait for it. Without the lock Claim returns at once with an empty queue.
func TestClaimWaitsForTheClaimLock(t *testing.T) {
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the queue tests (make up)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, claimLock); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := New(pool).Claim(ctx, "w", 1)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Claim returned (err %v) while another claimer held the lock", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Claim did not proceed after the lock was released")
	}
}
