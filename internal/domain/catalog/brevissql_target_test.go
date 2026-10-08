package catalog_test

import (
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// THE TARGETS brevis-sql EMITS, CHECKED HERE because they cannot be checked
// there.
//
// `sql/` is a module of its own and cannot import this package -- #66's own
// rule, so the engine's weight gate stays where it is. That leaves a gap
// with no edge: brevis-sql prints a `landed` line, the engine validates the
// target, and an invalid one is DROPPED AND COUNTED rather than refused.
// The step reports success, the build really happened, and the model simply
// never appears on `/data`. Nobody is told.
//
// So the strings are pinned on this side, copied from a real run of the
// binary against Postgres and BigQuery on 2026-10-08. If the rule in this
// package tightens, this test fails and names what it broke -- which is the
// only warning the other module can get.
func TestTheTargetsBrevisSQLEmitsAreValid(t *testing.T) {
	for _, target := range []string{
		// `postgres://database/schema/table`, the database read from the
		// DSN pgx already parsed -- never the DSN itself.
		"postgres://brevis_it/bvs_run_it/base",
		"postgres://analytics/staging/stg_orders",
		// `bigquery://project/dataset/table`. A project id carries hyphens,
		// and a domain-scoped one carries a colon.
		"bigquery://zarv-development-94b6/bvs_run_it/top",
		"bigquery://example.com:project/marts/orders",
	} {
		if err := catalog.ValidTarget(target, false); err != nil {
			t.Errorf("brevis-sql emits %q and this package refuses it: %v\n"+
				"    A refused target is dropped and counted, so the model would "+
				"build, report success and never reach /data.", target, err)
		}
	}
}

// And the shapes it must NEVER emit, so the test above is not passing
// because ValidTarget accepts everything.
func TestTheTargetsBrevisSQLMustNotEmitAreRefused(t *testing.T) {
	for _, target := range []string{
		"postgres://brevis:brevis@localhost:55432/brevis_it/s/t", // a DSN
		"postgres://brevis_it/bvs_run_it",                        // no table
		"postgres://brevis_it//base",                             // no schema
		"BigQuery://p/d/t",                                       // not lower-case
		"bvs_run_it.base",                                        // no scheme
	} {
		if err := catalog.ValidTarget(target, false); err == nil {
			t.Errorf("%q was accepted, so the test above proves nothing", target)
		}
	}
}
