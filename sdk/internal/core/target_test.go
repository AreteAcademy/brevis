package core

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// targetCase is one line of sdk/testdata/targets.txt.
type targetCase struct {
	verdict, target, reason string
	line                    int
}

// readTargetCases reads the shared fixture. The engine and the Python library
// keep copies of the same file; this is the original.
func readTargetCases(t *testing.T) []targetCase {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "targets.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var out []targetCase
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		raw := sc.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		body, reason, _ := strings.Cut(raw, " # ")
		verdict, target, _ := strings.Cut(body, " ")
		out = append(out, targetCase{verdict, strings.TrimSpace(target), reason, n})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("the fixture has no cases")
	}
	return out
}

func TestEveryFixtureTargetIsJudgedAsTheFixtureSays(t *testing.T) {
	for _, c := range readTargetCases(t) {
		err := CheckTarget(c.target)
		switch c.verdict {
		case "valid":
			if err != nil {
				t.Errorf("line %d: %q refused: %v", c.line, c.target, err)
			}
		case "pattern", "invalid":
			// A step lands on a table, never on a pattern: only a gateway's
			// published manifest may carry one, and the engine checks that.
			if err == nil {
				t.Errorf("line %d: %q accepted, want refused (%s)", c.line, c.target, c.reason)
			}
		default:
			t.Fatalf("line %d: unknown verdict %q", c.line, c.verdict)
		}
	}
}

func TestBigQueryTargetStripsThePartitionDecoratorAndBackticks(t *testing.T) {
	cases := []struct{ project, dataset, table, want string }{
		{"acme-prod", "bronze", "clicks", "bigquery://acme-prod/bronze/clicks"},
		{"acme-prod", "bronze", "clicks$20261007", "bigquery://acme-prod/bronze/clicks"},
		{"acme-prod", "bronze", "`clicks`", "bigquery://acme-prod/bronze/clicks"},
		{"example.com:acme", "bronze", "Clicks", "bigquery://example.com:acme/bronze/Clicks"},
		{"", "bronze", "clicks", ""},
		{"acme-prod", "bronze", "", ""},
	}
	for _, c := range cases {
		if got := BigQueryTarget(c.project, c.dataset, c.table); got != c.want {
			t.Errorf("BigQueryTarget(%q, %q, %q) = %q, want %q", c.project, c.dataset, c.table, got, c.want)
		}
	}
}

func TestPostgresIdentifiersFoldUnlessQuoted(t *testing.T) {
	cases := []struct{ scheme, db, schema, table, want string }{
		{"postgres", "analytics", "public", "Orders", "postgres://analytics/public/orders"},
		{"postgres", "analytics", `"Raw"`, `"Order Items"`, "postgres://analytics/Raw/Order%20Items"},
		{"redshift", "warehouse", "PUBLIC", "events", "redshift://warehouse/public/events"},
		{"postgres", "", "public", "orders", ""},
	}
	for _, c := range cases {
		if got := PostgresTarget(c.scheme, c.db, c.schema, c.table); got != c.want {
			t.Errorf("PostgresTarget(%q, %q, %q, %q) = %q, want %q", c.scheme, c.db, c.schema, c.table, got, c.want)
		}
	}
}

func TestMySQLAndPubSubTargets(t *testing.T) {
	if got := MySQLTarget("shop", "`Customers`"); got != "mysql://shop/Customers" {
		t.Errorf("MySQLTarget = %q", got)
	}
	if got := MySQLTarget("", "customers"); got != "" {
		t.Errorf("MySQLTarget without a database = %q, want empty", got)
	}
	if got := PubSubTarget("acme-prod", "clicks-raw"); got != "pubsub://acme-prod/clicks-raw" {
		t.Errorf("PubSubTarget = %q", got)
	}
}

func TestObjectTargetsNameThePrefixWithATrailingSlash(t *testing.T) {
	cases := []struct{ scheme, bucket, prefix, want string }{
		{"s3", "acme-landing", "vendors/dia=1", "s3://acme-landing/vendors/dia=1/"},
		{"s3", "acme-landing", "vendors/", "s3://acme-landing/vendors/"},
		{"gs", "acme-landing", "", "gs://acme-landing/"},
		{"file", "", "/var/data/out", "file:///var/data/out/"},
		{"file", "", "relative/out", ""},
		{"s3", "", "vendors/", ""},
	}
	for _, c := range cases {
		if got := ObjectTarget(c.scheme, c.bucket, c.prefix); got != c.want {
			t.Errorf("ObjectTarget(%q, %q, %q) = %q, want %q", c.scheme, c.bucket, c.prefix, got, c.want)
		}
	}
}

// Whatever a builder returns, the checker accepts. A builder that produced a
// target its own package refuses would land rows the engine then drops, and
// the table would simply never appear.
func TestEveryBuiltTargetPassesTheCheck(t *testing.T) {
	built := []string{
		BigQueryTarget("acme-prod", "bronze", "clicks$20261007"),
		BigQueryTarget("example.com:acme", "bronze", "clicks"),
		PostgresTarget("postgres", "analytics", `"We/ird"`, `"a@b?c#d e"`),
		PostgresTarget("redshift", "warehouse", "public", "events"),
		MySQLTarget("shop", "customers"),
		PubSubTarget("acme-prod", "clicks"),
		ObjectTarget("s3", "acme-landing", "vendors/dia=1 x"),
		ObjectTarget("file", "", "/tmp/out dir"),
	}
	for _, s := range built {
		if s == "" {
			t.Errorf("a builder returned empty for complete input")
			continue
		}
		if err := CheckTarget(s); err != nil {
			t.Errorf("built %q, and the check refuses it: %v", s, err)
		}
	}
}

func TestSplitQualifiedRespectsTheDialectsQuote(t *testing.T) {
	cases := []struct {
		name             string
		quote            byte
		qualifier, table string
		qualified        bool
	}{
		{"landing.orders", '"', "landing", "orders", true},
		{`"a.b".c`, '"', `"a.b"`, "c", true},
		{"orders", '"', "", "orders", false},
		{"`a.b`", '`', "", "`a.b`", false},
		{"`shop`.`Customers`", '`', "`shop`", "`Customers`", true},
	}
	for _, c := range cases {
		q, tb, ok := SplitQualified(c.name, c.quote)
		if q != c.qualifier || tb != c.table || ok != c.qualified {
			t.Errorf("SplitQualified(%q) = %q, %q, %v", c.name, q, tb, ok)
		}
	}
}
