package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// WHAT A WORKFLOW TALKS TO IS WHAT IT WROTE, recently: the scheme of each
// target its runs landed since the window began, once each, in order. A
// target with no scheme -- a legacy table name -- says nothing about where it
// lives, and one written before the window is history.
func TestConnectsIsTheSchemesAWorkflowWroteRecently(t *testing.T) {
	pool := insightsDB(t)
	now := time.Now()
	landAt(t, pool, "orders", "load", "bigquery://acme/bronze/orders", now.Add(-time.Hour), 0, n(1))
	landAt(t, pool, "orders", "load", "bigquery://acme/bronze/items", now.Add(-time.Hour), 0, n(1))
	landAt(t, pool, "orders", "export", "gs://acme-exports/orders.csv", now.Add(-2*time.Hour), 0, n(1))
	landAt(t, pool, "orders", "old", "postgres://warehouse/public/orders", now.Add(-100*24*time.Hour), 0, n(1))
	landAt(t, pool, "legacy", "load", "bronze_orders", now.Add(-time.Hour), 0, n(1))

	got, err := postgres.NewReadRepo(pool).Connects(context.Background(), now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["orders"], []string{"bigquery", "gs"}) {
		t.Errorf("orders connects to %v, want bigquery and gs", got["orders"])
	}
	if _, ok := got["legacy"]; ok {
		t.Errorf("a target with no scheme says where it lives: %v", got["legacy"])
	}
}
