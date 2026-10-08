package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write lays out a project on disk: path -> contents, relative to models/.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, "models", name)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A reference this project defines is an EDGE; everything else is a source.
// That distinction is the whole graph: one is built here and ordered, the
// other is somebody else's table that already exists.
func TestAReferenceIsAnEdgeOnlyIfItIsAModel(t *testing.T) {
	root := write(t, map[string]string{
		"bronze/orders.sql": "select * from raw.orders_landed",
		"silver/totals.sql": "select * from bronze.orders join raw.rates using (cur)",
	})
	p, err := Load(root, "postgres")
	if err != nil {
		t.Fatal(err)
	}

	if got := p.Edges["silver.totals"]; len(got) != 1 || got[0] != "bronze.orders" {
		t.Errorf("edges of silver.totals = %v, wanted [bronze.orders]", got)
	}
	if got := p.Sources["silver.totals"]; len(got) != 1 || got[0] != "raw.rates" {
		t.Errorf("sources of silver.totals = %v, wanted [raw.rates]", got)
	}
	if got := p.Edges["bronze.orders"]; len(got) != 0 {
		t.Errorf("bronze.orders reads only a source and has edges %v", got)
	}
}

func TestEverythingAModelReadsComesFirst(t *testing.T) {
	root := write(t, map[string]string{
		"gold/report.sql":   "select * from silver.totals",
		"bronze/orders.sql": "select * from raw.x",
		"silver/totals.sql": "select * from bronze.orders",
	})
	p, err := Load(root, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	order, err := p.Order()
	if err != nil {
		t.Fatal(err)
	}
	at := map[string]int{}
	for i, r := range order {
		at[r] = i
	}
	inOrder := at["bronze.orders"] < at["silver.totals"] &&
		at["silver.totals"] < at["gold.report"]
	if !inOrder {
		t.Errorf("order is %v", order)
	}
}

// A cycle names the RING. "there is a cycle" sends somebody to read every
// model in the project; the ring sends them to two.
func TestACycleNamesTheRing(t *testing.T) {
	root := write(t, map[string]string{
		"a/one.sql": "select * from b.two",
		"b/two.sql": "select * from a.one",
	})
	p, err := Load(root, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Order()
	if err == nil {
		t.Fatal("a ring was accepted")
	}
	for _, want := range []string{"a.one", "b.two", "->"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n  %v", want, err)
		}
	}
}

// depends_on is ADDED to what was inferred, never instead of it. A model that
// declares one edge the extractor cannot see still reads everything else it
// reads, and a header that replaced inference would silently drop the rest.
func TestDependsOnAddsToWhatWasInferred(t *testing.T) {
	root := write(t, map[string]string{
		"bronze/a.sql": "select 1",
		"bronze/b.sql": "select 1",
		"silver/x.sql": "/* brevis\ndepends_on: [bronze.b]\n*/\nselect * from bronze.a",
	})
	p, err := Load(root, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Edges["silver.x"], " ")
	if got != "bronze.a bronze.b" {
		t.Errorf("edges = %q, wanted both the inferred one and the declared one", got)
	}
}

// Two files cannot build one relation, and finding out at CREATE time means
// one of them silently won.
func TestTwoFilesCannotBeOneRelation(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"models/silver", "models/other"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Same schema directory name under different parents is not the case;
	// the real clash is the same schema/name pair, which needs one dir.
	if err := os.WriteFile(filepath.Join(root, "models/silver/x.sql"), []byte("select 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models/sub/silver"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models/sub/silver/x.sql"), []byte("select 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "postgres"); err == nil || !strings.Contains(err.Error(), "silver.x") {
		t.Errorf("two files building silver.x were accepted: %v", err)
	}
}

// `name+` is the question somebody actually asks: I changed this, what else
// moves.
func TestSelectDownstream(t *testing.T) {
	root := write(t, map[string]string{
		"bronze/a.sql":   "select * from raw.x",
		"silver/b.sql":   "select * from bronze.a",
		"gold/c.sql":     "select * from silver.b",
		"gold/alone.sql": "select * from raw.y",
	})
	p, err := Load(root, "postgres")
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.Select("silver.b+")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "silver.b gold.c" {
		t.Errorf("silver.b+ = %v, wanted silver.b then gold.c", got)
	}

	one, err := p.Select("silver.b")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0] != "silver.b" {
		t.Errorf("silver.b = %v, wanted just itself", one)
	}

	if _, err := p.Select("nope.nope+"); err == nil {
		t.Error("a --select naming no model was accepted")
	}
}

// THE GRAPH OF A REAL PROJECT, pinned edge by edge.
//
// jaffle-shop is dbt's own example, ported in the spike, and it is #62's
// acceptance fixture. Thirteen models with a DAG somebody else designed: the
// extractor had never seen them and got every edge right, which is a
// different claim from thirty corpus cases written to exercise it.
//
// Written out in full rather than counted. A test that says "13 models, 17
// edges" passes with the wrong 17, and a wrong edge is exactly the failure
// `brevis-sql graph` exists to make visible.
func TestTheJaffleShopGraph(t *testing.T) {
	p, err := Load("testdata/jaffle", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 13 {
		t.Fatalf("loaded %d models, wanted 13", len(p.Models))
	}

	want := map[string]string{
		"staging.stg_customers":       "",
		"staging.stg_locations":       "",
		"staging.stg_order_items":     "",
		"staging.stg_orders":          "",
		"staging.stg_products":        "",
		"staging.stg_supplies":        "",
		"marts.metricflow_time_spine": "",
		"marts.locations":             "staging.stg_locations",
		"marts.products":              "staging.stg_products",
		"marts.supplies":              "staging.stg_supplies",
		"marts.order_items":           "staging.stg_order_items staging.stg_orders staging.stg_products staging.stg_supplies",
		"marts.orders":                "marts.order_items staging.stg_orders",
		"marts.customers":             "marts.orders staging.stg_customers",
	}
	for ref, edges := range want {
		if _, ok := p.Models[ref]; !ok {
			t.Errorf("%s is missing from the project", ref)
			continue
		}
		if got := strings.Join(p.Edges[ref], " "); got != edges {
			t.Errorf("%s reads %q, wanted %q", ref, got, edges)
		}
	}

	// The staging models read raw tables nobody here builds, and calling one
	// of those an edge would make the project wait for something it cannot
	// create.
	if got := strings.Join(p.Sources["staging.stg_orders"], " "); got != "raw.raw_orders" {
		t.Errorf("sources of staging.stg_orders = %q", got)
	}

	order, err := p.Order()
	if err != nil {
		t.Fatal(err)
	}
	at := map[string]int{}
	for i, r := range order {
		at[r] = i
	}
	for _, pair := range [][2]string{
		{"staging.stg_orders", "marts.order_items"},
		{"marts.order_items", "marts.orders"},
		{"marts.orders", "marts.customers"},
	} {
		if at[pair[0]] >= at[pair[1]] {
			t.Errorf("%s must be built before %s", pair[0], pair[1])
		}
	}
}
