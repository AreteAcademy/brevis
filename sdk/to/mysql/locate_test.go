package mysql

import (
	"strings"
	"testing"
)

func TestLocateNamesDatabaseAndTableAndNothingElse(t *testing.T) {
	const dsn = "loader:s3cret@tcp(db.internal:3306)/shop?parseTime=true"
	cases := []struct{ name, dsn, table, want string }{
		{"the DSN's database", dsn, "customers", "mysql://shop/customers"},
		{"a qualified name wins", dsn, "archive.customers", "mysql://archive/customers"},
		{"backticks", dsn, "`archive`.`Customers`", "mysql://archive/Customers"},
		{"a dot inside backticks is not a separator", dsn, "`a.b`", "mysql://shop/a.b"},
		{"no database in the DSN and none in the name", "loader:x@tcp(db.internal:3306)/", "customers", ""},
		{"no DSN", "", "customers", ""},
		{"an unparseable DSN", "not a dsn at all(", "customers", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Table{DSN: c.dsn, Name: c.table}.Locate()
			if got != c.want {
				t.Fatalf("Locate() = %q, want %q", got, c.want)
			}
			for _, leak := range []string{"loader", "s3cret", "db.internal", "3306"} {
				if strings.Contains(got, leak) {
					t.Fatalf("Locate() = %q carries %q", got, leak)
				}
			}
		})
	}
}

// A pool handed in names a database only by asking the server, and Locate asks
// nothing: a qualified Name is the way to land from a reused pool.
func TestLocateWithAPoolNeedsAQualifiedName(t *testing.T) {
	if got := (Table{Name: "customers"}).Locate(); got != "" {
		t.Fatalf("Locate() = %q, want empty", got)
	}
	if got := (Table{Name: "shop.customers"}).Locate(); got != "mysql://shop/customers" {
		t.Fatalf("Locate() = %q", got)
	}
}
