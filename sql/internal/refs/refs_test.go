package refs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The corpus, as files rather than as a list in Go.
//
// `testdata/<dialect>/NN.sql` with `NN.want` beside it, one reference per
// line. The labelling rule is in testdata/README.md, because expectations
// nobody can read are expectations nobody can correct — and correcting one is
// how this suite is supposed to grow when a model's inferred edge is wrong.
func TestTheCorpus(t *testing.T) {
	for _, dialect := range []string{"postgres", "bigquery"} {
		t.Run(dialect, func(t *testing.T) {
			queries, err := filepath.Glob(filepath.Join("testdata", dialect, "*.sql"))
			if err != nil {
				t.Fatal(err)
			}
			if len(queries) != 15 {
				t.Fatalf("found %d cases for %s, and the corpus has 15", len(queries), dialect)
			}

			for _, q := range queries {
				t.Run(filepath.Base(q), func(t *testing.T) {
					sql, err := os.ReadFile(q)
					if err != nil {
						t.Fatal(err)
					}
					wantRaw, err := os.ReadFile(strings.TrimSuffix(q, ".sql") + ".want")
					if err != nil {
						t.Fatal(err)
					}

					got, err := Of(dialect, string(sql))
					if err != nil {
						t.Fatalf("extracting: %v", err)
					}
					if g, w := normalise(got), normalise(splitLines(string(wantRaw))); g != w {
						t.Errorf("references differ\n got: %s\nwant: %s\n\nthe query:\n%s", g, w, sql)
					}
				})
			}
		})
	}
}

// A CTE is not a reference, and neither is an alias. Pinned here rather than
// left to the corpus, because these two are the rules the extractor exists to
// apply and a corpus case can be deleted by accident.
func TestWhatIsNotAReference(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{
			name: "a CTE is defined, not read",
			sql:  "with recent as (select * from raw.orders) select * from recent",
			want: "raw.orders",
		},
		{
			name: "an alias is not a table",
			sql:  "select o.id from raw.orders o join raw.items i on i.oid = o.id",
			want: "raw.items raw.orders",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Of("postgres", c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if g := normalise(got); g != c.want {
				t.Errorf("got %q, wanted %q", g, c.want)
			}
		})
	}
}

// Postgres folds an unquoted identifier to lower case, so `RAW.Orders` and
// `raw.orders` are ONE relation and a model reading both has one edge.
//
// Pinned here and not added to the corpus, for two reasons. The corpus was
// labelled before any candidate existed and that provenance is the only
// reason its score means anything — growing it now would spend that. And a
// mutation proved this needed its own test: deleting the folding rule from
// the extractor leaves all thirty cases green, while the spike measured that
// same rule taking Postgres from 86.8% to 98.8% on held-out SQL. The most
// valuable rule in this package was the one nothing checked.
func TestPostgresFoldsAnUnquotedIdentifier(t *testing.T) {
	got, err := Of("postgres", `select * from RAW.Orders o join raw.orders b on b.id = o.id`)
	if err != nil {
		t.Fatal(err)
	}
	if g := normalise(got); g != "raw.orders" {
		t.Errorf("got %q, wanted one relation %q — Postgres folds unquoted names", g, "raw.orders")
	}
}

// And BigQuery does NOT fold, so the same query there is two relations.
// Without this the rule above could be applied to both dialects and nothing
// would say otherwise.
func TestBigQueryKeepsTheCase(t *testing.T) {
	got, err := Of("bigquery", "select * from `RAW.Orders` o join `raw.orders` b on b.id = o.id")
	if err != nil {
		t.Fatal(err)
	}
	if g := normalise(got); g != "RAW.Orders raw.orders" {
		t.Errorf("got %q, wanted both — BigQuery table names are case-sensitive", g)
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Sorted and joined, because the ORDER a query mentions its tables in is not
// something a dependency edge cares about.
func normalise(in []string) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range in {
		if r = strings.TrimSpace(r); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, " ")
}
