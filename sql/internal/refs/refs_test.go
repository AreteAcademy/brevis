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

// CHAINED CTEs, which nothing covered. The suite had one `with a as (...)`
// and stopped there, so the whole second-and-later-CTE path -- readCTEs
// calling itself at the comma -- was carried by a single corpus case nobody
// had looked at.
//
// Written because a linter asked a question the tests could not answer. The
// `for` in readCTEs returns on every path, which staticcheck reported as a
// loop that never loops; whether that was a dead shape or a real defect
// depended on the recursion working, and the only honest way to find out was
// to ask it. It works, and the `for` became an `if` -- but the four cases
// below are the reason that is known rather than assumed.
//
// `not materialized` is here because it is the one place a CTE's keyword run
// is longer than a word: a reader that skips one token lands on `(` and
// gives up, silently turning the rest of the query into nothing.
func TestChainedCTEsAreAllDefinitions(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{
			name: "two CTEs, and neither is a reference",
			sql:  "with a as (select * from raw.x), b as (select * from raw.y) select * from a join b on 1=1",
			want: "raw.x raw.y",
		},
		{
			name: "a CTE reading an earlier CTE is still not a reference",
			sql:  "with a as (select * from raw.x), b as (select * from a), c as (select * from raw.z) select * from c",
			want: "raw.x raw.z",
		},
		{
			name: "a later CTE whose body reads nothing",
			sql:  "with a as (select 1), b as (select * from raw.y) select * from a join b on 1=1",
			want: "raw.y",
		},
		{
			// A CTE AFTER the `not materialized` one, which is the only
			// place that keyword pair changes an answer. Without it the
			// reader stops at `not`, never reaches the comma, and `c` is
			// never recorded as a definition -- so it comes back as a
			// table that does not exist and the model waits on an edge to
			// nothing. With only two CTEs the bug is invisible: the last
			// name is already recorded before the keywords are read.
			name: "a CTE follows a not materialized one",
			sql: "with a as materialized (select * from raw.x), " +
				"b as not materialized (select * from raw.y), " +
				"c as (select * from raw.z) select * from a, b, c",
			want: "raw.x raw.y raw.z",
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
