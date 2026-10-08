package model

import (
	"strings"
	"testing"
)

const withHeader = `/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
*/
select o.order_id, sum(i.amount) as total
from bronze.orders o join bronze.order_items i using (order_id)
group by 1`

func TestAModelIsItsPathAndItsHeader(t *testing.T) {
	m, err := Parse("models/silver/order_totals.sql", []byte(withHeader))
	if err != nil {
		t.Fatal(err)
	}
	if m.Ref() != "silver.order_totals" {
		t.Errorf("Ref() = %q, wanted silver.order_totals", m.Ref())
	}
	if m.Materialised != Table {
		t.Errorf("materialised = %q, wanted table", m.Materialised)
	}
	if len(m.Tests) != 2 || m.Tests[0].Kind != "not_null" || m.Tests[1].Kind != "unique" {
		t.Errorf("tests = %+v", m.Tests)
	}
	// The header stays in the SQL: it is a comment, so the file that ran is
	// the file on disk and a warehouse's query log shows what somebody wrote.
	if !strings.Contains(m.SQL, "/* brevis") {
		t.Error("the header was stripped from the SQL")
	}
}

// A model with no header is a view with no tests, not an error. Six lines of
// ceremony before the first query runs is how a format gets a reputation.
func TestAModelWithNoHeaderIsAView(t *testing.T) {
	m, err := Parse("models/silver/plain.sql", []byte("select 1"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Materialised != View || len(m.Tests) != 0 {
		t.Errorf("got %q with %d tests, wanted a view with none", m.Materialised, len(m.Tests))
	}
}

// A `/* brevis */` halfway down is a sentence somebody wrote about the query.
// Reading it as config would turn a comment into a setting.
func TestOnlyALeadingHeaderCounts(t *testing.T) {
	m, err := Parse("models/silver/x.sql", []byte("select 1\n/* brevis\nmaterialized: table\n*/"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Materialised != View {
		t.Errorf("a comment below the query set materialized=%q", m.Materialised)
	}
}

// Every refusal names the FILE, and the ones about the header name the line.
// An error about a config file that does not say where to look is an error
// somebody reads twice.
func TestEveryRefusalSaysWhereToLook(t *testing.T) {
	for _, c := range []struct{ name, path, sql, want string }{
		{
			name: "a typo in a field",
			path: "models/silver/a.sql",
			sql:  "/* brevis\nmaterialised: table\n*/\nselect 1",
			want: "materialised",
		},
		{
			name: "a materialisation that does not exist",
			path: "models/silver/b.sql",
			sql:  "/* brevis\nmaterialized: ephemeral\n*/\nselect 1",
			want: "`view` or `table`",
		},
		{
			name: "a test nobody implements",
			path: "models/silver/c.sql",
			sql:  "/* brevis\ntests:\n  - no_nulls_pls: [id]\n*/\nselect 1",
			want: "not a test this knows",
		},
		{
			name: "a model outside models/<schema>/",
			path: "loose.sql",
			want: "models/<schema>/<name>.sql",
			sql:  "select 1",
		},
		{
			name: "not a .sql at all",
			path: "models/silver/d.txt",
			sql:  "select 1",
			want: "a `.sql` file",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.path, []byte(c.sql))
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the message does not say %q:\n  %v", c.want, err)
			}
			if !strings.Contains(err.Error(), c.path) {
				t.Errorf("the message does not name the file:\n  %v", err)
			}
		})
	}
}

// depends_on is the override for an edge the extractor cannot see, which is
// the whole reason it exists -- SQL built at runtime, or one of the forms
// refs names as its limits.
func TestDependsOnIsCarried(t *testing.T) {
	m, err := Parse("models/silver/x.sql", []byte("/* brevis\ndepends_on: [bronze.weird]\n*/\nselect 1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.DependsOn) != 1 || m.DependsOn[0] != "bronze.weird" {
		t.Errorf("depends_on = %v", m.DependsOn)
	}
}
