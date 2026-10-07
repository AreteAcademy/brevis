package gateway_test

import (
	osexec "os/exec"
	"strings"
	"testing"
)

// The slim build must not link the drivers it does not register.
//
// This is the whole claim of the registry, and it is the one thing no ordinary
// test can see: everything still compiles and every test passes whether or not
// the linker actually pruned anything. The `switch` this replaced kept all six
// drivers in every binary and nothing anywhere said so -- the cost showed up
// as a number in a docker pull.
//
// So the assertion is on the DEPENDENCY GRAPH: cmd/gateway-slim must not reach
// the AWS SDK, the Google stack or Arrow. If somebody adds an import to the
// root package that drags one of them in -- a convenience helper, a shared
// type -- every test here still passes and this one fails, which is the point.
func TestTheSlimBuildDoesNotCarryWhatItDoesNotRegister(t *testing.T) {
	deps := dependenciesOf(t, "./cmd/gateway-slim")

	for _, forbidden := range []string{
		"github.com/aws/aws-sdk-go-v2",   // ~3 MB, and only store/s3 needs it
		"cloud.google.com/go/storage",    // the GCS backend
		"cloud.google.com/go/pubsub",     // the Pub/Sub sink
		"cloud.google.com/go/bigquery",   // the BigQuery sink
		"github.com/apache/arrow",        // which BigQuery drags in, ~6 MB with it
		"github.com/go-sql-driver/mysql", // the MySQL sink
		"github.com/redis/go-redis",      // the Redis metastore
		"github.com/bradfitz/gomemcache", // the memcached metastore
	} {
		for _, d := range deps {
			if strings.HasPrefix(d, forbidden) {
				t.Errorf("the slim build links %s, through %s: it registers "+
					"neither the sink nor the store that needs it, so something "+
					"in the packages it DOES import is reaching for it",
					forbidden, d)
				break
			}
		}
	}

	// And the other half: it has to carry what it does register, or this test
	// would pass on a binary that links nothing because it does not build.
	if !contains(deps, "github.com/jackc/pgx/v5") {
		t.Error("the slim build does not link pgx, and it registers the Postgres sink")
	}
}

// The full build carries all six, which is what makes it the one that "just
// works" -- and what makes it 49 MB.
func TestTheFullBuildCarriesEverything(t *testing.T) {
	deps := dependenciesOf(t, "./cmd/gateway")
	for _, required := range []string{
		"github.com/jackc/pgx/v5",
		"github.com/go-sql-driver/mysql",
		"cloud.google.com/go/pubsub",
		"cloud.google.com/go/bigquery",
		"cloud.google.com/go/storage",
		"github.com/aws/aws-sdk-go-v2/service/s3",
		"github.com/redis/go-redis/v9",
		"github.com/bradfitz/gomemcache/memcache",
	} {
		if !contains(deps, required) {
			t.Errorf("the published image does not link %s, so a config naming "+
				"its sink would be refused by an image that claims to carry it",
				required)
		}
	}
}

func dependenciesOf(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := osexec.Command("go", "list", "-deps", pkg).Output() //nolint:gosec // a literal
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func contains(deps []string, prefix string) bool {
	for _, d := range deps {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}
