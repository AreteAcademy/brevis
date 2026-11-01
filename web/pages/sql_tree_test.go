package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// sqlFixture is a workbench over two BigQuery datasets on one project, a
// Postgres database and a Redshift cluster, open on the BigQuery project with
// its listing drawn.
func sqlFixture() SQLView {
	return SQLView{
		Targets: []string{"bigquery://acme/bronze/orders", "bigquery://acme/silver/daily",
			"postgres://shop/public/invoices", "redshift://cluster/sales/events"},
		Target: "bigquery://acme/bronze/orders",
		Tree:   []Schema{{Name: "bronze", Tables: []string{"orders", "refunds"}}, {Name: "silver", Tables: []string{"daily"}}},
	}
}

// THE CONNECTION IS A PICKER AT THE TOP OF THE TREE (screen-sql T1): one
// choice per connection, the chosen one first-class with its logo, and the
// ones this console cannot read offered disabled with the reason -- the
// service reads only Postgres and BigQuery.
func TestTheConnectionPickerOffersWhatTheConsoleReads(t *testing.T) {
	page := drawIn(t, "en", SQL(sqlFixture()))
	pick := page[strings.Index(page, "data-connection-pick"):]
	pick = pick[:strings.Index(pick, "</form>")]
	for _, want := range []string{
		`name="connection"`, `<option value="bigquery://acme/bronze/orders" selected`, `<option value="postgres://shop/public/invoices"`,
		`aria-label="Google BigQuery"`, "reads only Postgres and BigQuery",
	} {
		if !strings.Contains(pick, want) {
			t.Errorf("the picker lacks %s", want)
		}
	}
	if strings.Count(pick, "<option") != 3 {
		t.Errorf("one choice per connection: %d", strings.Count(pick, "<option"))
	}
	redshift := pick[strings.Index(pick, `value="redshift://cluster/sales/events"`):]
	if !strings.HasPrefix(redshift[strings.Index(redshift, " ")+1:], "disabled") {
		t.Errorf("Redshift is offered: %s", redshift[:80])
	}
	if strings.Contains(page, `name="connect"`) {
		t.Error("the connections are still roots of the tree")
	}
}

// A TABLE IN THE TREE IS A SELECT, as a build would name it: the dataset or
// schema and the table, a hundred rows. Clicking it opens its columns and
// puts that SELECT in the editor; a destination that is not a table has
// none.
func TestATableIsASelect(t *testing.T) {
	for target, want := range map[string]string{
		"bigquery://acme/bronze/orders":   "SELECT *\nFROM bronze.orders\nLIMIT 100;",
		"postgres://shop/public/invoices": "SELECT *\nFROM public.invoices\nLIMIT 100;",
		"bigquery://acme":                 "",
		"redshift://cluster/sales/events": "",
	} {
		if got := SelectFor(target); got != want {
			t.Errorf("%s: %q", target, got)
		}
	}
	tree := drawIn(t, "en", Objects(sqlFixture().Branch()))
	if !strings.Contains(tree, `data-select="SELECT *`+"\n"+`FROM bronze.refunds`) {
		t.Error("a table in the tree does not carry its SELECT")
	}
}

// THE CONNECTIONS THIS CONSOLE CANNOT READ ARE NEVER THE ONE IT OPENS ON.
func TestTheWorkbenchOpensOnAConnectionItReads(t *testing.T) {
	if !Queryable("bigquery://acme/bronze/orders") || !Queryable("postgres://shop/public/invoices") ||
		Queryable("redshift://cluster/sales/events") || Queryable("mysql://shop/orders") {
		t.Error("Queryable")
	}
	v := SQLView{Targets: []string{"redshift://cluster/sales/events", "postgres://shop/public/invoices"}}
	if got := v.FirstQueryable(); got != "postgres://shop/public/invoices" {
		t.Errorf("opens on %q", got)
	}
}

// THE TREE SPEAKS THE READER'S LANGUAGE; the names in it are the warehouse's.
func TestTheTreeHasNoEnglishLeft(t *testing.T) {
	v := sqlFixture()
	v.TreeCut, v.Open, v.Cols = true, "bronze.orders", nil
	text := visibleText(drawIn(t, "pt-BR", SQL(v))) + " " + visibleText(drawIn(t, "pt-BR", Columns([]sqlserve.Column{})))
	for _, english := range []string{"Objects", "Search this tree", "Filter tables", "Collapse the object tree",
		"Show the object tree", "columns", "This warehouse does not say", "Showing part of this warehouse",
		"reads only", "Connection", "Resize the object tree"} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese tree still says %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
