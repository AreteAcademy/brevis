package redshift

import (
	"strings"
	"testing"
)

func TestLocateNamesDatabaseSchemaAndTableAndNothingElse(t *testing.T) {
	const dsn = "postgres://loader:s3cret@cluster.abc.us-east-1.redshift.amazonaws.com:5439/warehouse"
	cases := []struct{ name, dsn, table, want string }{
		{"qualified", dsn, "public.events", "redshift://warehouse/public/events"},
		{"folded", dsn, "Public.Events", "redshift://warehouse/public/events"},
		{"unqualified falls to public", dsn, "events", "redshift://warehouse/public/events"},
		{"search_path in the DSN", dsn + "?search_path=raw", "events", "redshift://warehouse/raw/events"},
		{"no DSN", "", "public.events", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Table{DSN: c.dsn, Name: c.table}.Locate()
			if got != c.want {
				t.Fatalf("Locate() = %q, want %q", got, c.want)
			}
			for _, leak := range []string{"loader", "s3cret", "amazonaws", "5439"} {
				if strings.Contains(got, leak) {
					t.Fatalf("Locate() = %q carries %q", got, leak)
				}
			}
		})
	}
}
