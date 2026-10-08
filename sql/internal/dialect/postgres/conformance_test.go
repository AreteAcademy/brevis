package postgres_test

import (
	"os"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// The conformance suite, against a real server.
//
// It is the only test in this module that needs one, and it is gated the way
// the SDK's integration tests are -- by the DSN's own environment variable,
// so a developer with no Postgres sees a skip and not a failure:
//
//	docker compose -f docker-compose.drivers.yml --profile sql up -d postgres
//	BREVIS_SQL_IT_DSN=postgres://brevis:brevis@localhost:55432/brevis_it \
//	  go test ./internal/dialect/postgres/
func TestPostgresConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped under -short")
	}
	dsn := os.Getenv("BREVIS_SQL_IT_DSN")
	if dsn == "" {
		t.Skip("BREVIS_SQL_IT_DSN not set; skipping the conformance suite")
	}
	dialecttest.Run(t, postgres.Dialect{}, dsn)
}
