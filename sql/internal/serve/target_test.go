package serve

import (
	"strings"
	"testing"
)

// A TARGET BECOMES SQL, so parsing one is the most dangerous line here.
//
// `bigquery://acme/bronze/orders` has to become the project `acme` and the
// relation `bronze.orders`, and ANYTHING else has to become a refusal. The
// pieces are not escaped and then used -- they are refused unless they are
// already names BigQuery could hold, which is the rule the dialect's own
// Build applies before writing DDL.
func TestATargetBecomesAProjectAndARelation(t *testing.T) {
	got, err := ParseTarget("bigquery://acme-prod/bronze/orders")
	if err != nil {
		t.Fatal(err)
	}
	if got.Connection != "acme-prod" {
		t.Errorf("connection = %q", got.Connection)
	}
	if got.Relation != "bronze.orders" {
		t.Errorf("relation = %q", got.Relation)
	}
}

// EVERY ONE OF THESE IS REFUSED, and the list is the point.
//
// The first four are injection: a semicolon, a comment, a backtick and a
// space are each enough to turn one statement into two, or to end a name
// early and start something else. The rest are shapes the catalog itself
// admits for things that are NOT tables -- an object in a bucket, a Pub/Sub
// topic -- which have no rows to preview and must not reach SQL at all.
func TestEverythingElseIsRefused(t *testing.T) {
	// THE PROJECT IS VALID IN EVERY ONE OF THESE, on purpose. The first
	// version used `acme`, which is four characters and fails the project
	// rule on its own -- so every injection case was refused before the
	// dataset or the table was ever looked at, and the identifier guard,
	// which is the one that matters, was never exercised. A mutation that
	// deleted it should have failed this test and did not.
	for _, bad := range []string{
		"bigquery://acme-prod/bronze/orders; DROP TABLE x",
		"bigquery://acme-prod/bronze/orders--",
		"bigquery://acme-prod/bronze/`orders`",
		"bigquery://acme-prod/bronze/or ders",
		"bigquery://acme-prod/bronze/orders/extra",
		"bigquery://acme-prod/bronze",
		"bigquery://acme-prod//orders",
		"bigquery://acme-prod/bron ze/orders",
		"bigquery://acme-prod/bronze/orders\nUNION ALL SELECT 1",
		"bigquery://",
		"s3://bucket/path/file.parquet",
		"gs://bucket/path",
		"file:///tmp/out.csv",
		"pubsub://acme/topic",
		"postgres://db/public/orders",
		"mysql://db/orders",
		"",
		"not a target at all",
		"bigquery://acme-prod/bronze/1orders",
		"bigquery://sh/bronze/orders", // the project rule, still its own case
	} {
		t.Run(bad, func(t *testing.T) {
			got, err := ParseTarget(bad)
			if err == nil {
				t.Fatalf("accepted %q as %+v", bad, got)
			}
		})
	}
}

// A REFUSAL NAMES THE TARGET AND NOT THE REASON IT GUESSED. The message
// reaches a screen, so it says what was given and what shape is wanted --
// never a fragment of the input pasted into prose, which is how a refusal
// becomes the injection it refused.
func TestARefusalSaysWhatShapeItWanted(t *testing.T) {
	_, err := ParseTarget("postgres://db/public/orders")
	if err == nil {
		t.Fatal("accepted a Postgres target")
	}
	if !strings.Contains(err.Error(), "bigquery://") {
		t.Errorf("the refusal does not say what is supported: %v", err)
	}
}
