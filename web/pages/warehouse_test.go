package pages

import "testing"

// A TARGET IS NOT A CONNECTION, and the tree is about connections.
//
// `serve` reads a target as `scheme://connection/schema/table` and opens on
// the scheme and the FIRST segment -- `/v1/query` never looks at the rest. So
// two destinations in two datasets of one project are ONE warehouse, and a
// tree with a root per target would draw it twice with identical contents
// under each.
func TestTwoDestinationsOnOneProjectAreOneWarehouse(t *testing.T) {
	got := Warehouses([]string{
		"bigquery://acme-prod/bronze/orders",
		"bigquery://acme-prod/silver/daily",
		"postgres://warehouse/public/events",
	})
	if len(got) != 2 {
		t.Fatalf("grouped into %d roots, wanted 2: %+v", len(got), got)
	}
	if got[0].Name != "bigquery://acme-prod" {
		t.Errorf("the first root is %q", got[0].Name)
	}
	if got[1].Name != "postgres://warehouse" {
		t.Errorf("the second root is %q", got[1].Name)
	}
	// AND IT KEEPS A WHOLE TARGET, because that is what every request
	// carries: the service parses the target itself, so the grouping is a
	// label and never the thing that is sent.
	if got[0].Target != "bigquery://acme-prod/bronze/orders" {
		t.Errorf("the root carries %q, which is not a destination", got[0].Target)
	}
}

// ORDER IS THE CATALOG'S. The listing is already ordered where it is built,
// and a second opinion here would be a second ordering nobody asked for --
// the same rule BuildTree states about schemas.
func TestTheRootsKeepTheCatalogsOrder(t *testing.T) {
	got := Warehouses([]string{
		"postgres://zed/public/a",
		"bigquery://alpha/x/y",
	})
	if len(got) != 2 || got[0].Name != "postgres://zed" {
		t.Errorf("the roots were re-ordered: %+v", got)
	}
}

// A STRING THAT IS NOT A DESTINATION IS NOT A ROOT. The catalog holds what
// Brevis wrote, and `IsRelation` has already dropped the buckets by the time
// this runs -- but a root built from something with no scheme would be a node
// that reaches nothing.
func TestSomethingWithNoSchemeIsNotARoot(t *testing.T) {
	if got := Warehouses([]string{"not-a-target"}); len(got) != 0 {
		t.Errorf("built a root out of %+v", got)
	}
}
