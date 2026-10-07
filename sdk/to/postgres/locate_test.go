package postgres

import (
	"strings"
	"testing"
)

func TestLocateNamesDatabaseSchemaAndTableAndNothingElse(t *testing.T) {
	const url = "postgres://loader:s3cret@db.internal:5432/analytics?sslmode=disable"
	cases := []struct {
		name, dsn, table, want string
	}{
		{"qualified", url, "landing.orders", "postgres://analytics/landing/orders"},
		{"unqualified falls to public", url, "orders", "postgres://analytics/public/orders"},
		{"folded like the database folds", url, "Landing.Orders", "postgres://analytics/landing/orders"},
		{"quoted keeps case", url, `"Raw"."Order Items"`, "postgres://analytics/Raw/Order%20Items"},
		{"a dot inside quotes is not a separator", url, `"a.b".c`, "postgres://analytics/a.b/c"},
		{"keyword DSN", "host=db.internal user=loader password=x dbname=analytics", "landing.orders",
			"postgres://analytics/landing/orders"},
		{"search_path in the URL", url + "&search_path=raw", "orders", "postgres://analytics/raw/orders"},
		{"search_path through options", "host=h dbname=analytics options='-c search_path=raw,public'", "orders",
			"postgres://analytics/raw/orders"},
		{"$user is skipped", url + "&search_path=%22$user%22,staging", "orders", "postgres://analytics/staging/orders"},
		{"no DSN and no Conn", "", "landing.orders", ""},
		{"an unparseable DSN", "postgres://%zz", "landing.orders", ""},
		{"no name", url, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Table{DSN: c.dsn, Name: c.table}.Locate()
			if got != c.want {
				t.Fatalf("Locate() = %q, want %q", got, c.want)
			}
			for _, leak := range []string{"loader", "s3cret", "db.internal", "5432", "password"} {
				if strings.Contains(got, leak) {
					t.Fatalf("Locate() = %q carries %q: a target is a name, never an address", got, leak)
				}
			}
		})
	}
}
