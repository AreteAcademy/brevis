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
		"mysql://db/orders",
		"redshift://cluster/public/orders",
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
// A REFUSAL SAYS WHAT SHAPE IT WANTED, and now there are two of them.
//
// It asked about a Postgres target while BigQuery was the only readable
// scheme. V3 made that one valid, so the case is a scheme that still is not,
// and the refusal has to name BOTH shapes rather than the one it used to.
func TestARefusalSaysWhatShapeItWanted(t *testing.T) {
	_, err := ParseTarget("redshift://cluster/public/orders")
	if err == nil {
		t.Fatal("accepted a Redshift target")
	}
	for _, want := range []string{"bigquery://", "postgres://", "redshift"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q: %v", want, err)
		}
	}
}

// A POSTGRES TARGET IS A DATABASE AND A RELATION.
//
// `postgres://brevis/public/orders` is the database `brevis` and the relation
// `public.orders`. The first segment is NOT a project and not an address: a
// target is an identity, and the host, port and user live in a connection
// somebody declared. That split is the whole reason V3 exists.
func TestAPostgresTargetBecomesADatabaseAndARelation(t *testing.T) {
	got, err := ParseTarget("postgres://brevis/public/orders")
	if err != nil {
		t.Fatalf("a real Postgres target was refused: %v", err)
	}
	if got.Connection != "brevis" {
		t.Errorf("the connection is %q, and the database is `brevis`", got.Connection)
	}
	if got.Dialect != "postgres" {
		t.Errorf("the dialect is %q", got.Dialect)
	}
	if got.Relation != "public.orders" {
		t.Errorf("the relation is %q", got.Relation)
	}
}

// A DATABASE NAME TAKES DASHES AND A PROJECT ID DOES NOT TAKE UNDERSCORES,
// so the two schemes cannot share one rule. Both are what the dialects
// actually produce: `brevis_it` comes out of a DSN, `acme-prod` out of a
// Google project.
func TestEachSchemeKeepsItsOwnNameRule(t *testing.T) {
	for _, ok := range []string{
		"postgres://brevis_it/public/orders",
		"postgres://analytics-prod/silver/daily",
		"postgres://b/public/orders",
		"bigquery://acme-prod/bronze/orders",
	} {
		if _, err := ParseTarget(ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		// The relation halves reach SQL and keep the strict rule.
		"postgres://brevis/public/orders; DROP TABLE t",
		"postgres://brevis/pub lic/orders",
		"postgres://brevis/public/or\"ders",
		"postgres://brevis/public/orders`",
		// The database never reaches SQL, and is still not a free-for-all.
		"postgres://bre vis/public/orders",
		"postgres://bre/vis/public/orders",
		"postgres://@host/public/orders",
		"postgres://brevis\n/public/orders",
		// Two segments is a MySQL shape, and this is not MySQL.
		"postgres://brevis/orders",
		// Still refused, because no connection can be matched to them.
		"mysql://brevis/orders",
		"redshift://cluster/public/orders",
		"s3://bucket/prefix",
	} {
		if got, err := ParseTarget(bad); err == nil {
			t.Errorf("ACCEPTED %q -> %+v", bad, got)
		}
	}
}

// THE REFUSAL STILL NAMES NO INPUT, for either scheme.
func TestAPostgresRefusalDoesNotEchoTheTarget(t *testing.T) {
	const secret = "cpf_12345678900"
	_, err := ParseTarget("postgres://" + secret + " /public/orders")
	if err == nil {
		t.Fatal("accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the refusal repeated the target: %v", err)
	}
}
