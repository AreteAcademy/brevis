package pages

import (
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// SQLView is the workbench.
//
// A SCREEN OF ITS OWN, AND THAT IS THE POINT. `/data` answers "what did the
// pipelines land, and is it on time"; this answers "what is in it". The
// second question is not asked one table at a time, which is what a tab on a
// destination page forces.
type SQLView struct {
	// Targets are the destinations a SELECT could name, from the catalog.
	// A bucket has no columns, and offering one here is the same mistake as
	// drawing a Query tab on it.
	Targets []string

	// Target is the one chosen, Statement what is in the box -- ECHOED BACK,
	// because a query refused for a typo with the box emptied is a query
	// somebody has to type again.
	Target    string
	Statement string

	Result *sqlserve.Result
	Err    string

	// Tree is what the chosen connection holds, grouped by schema. Empty
	// when the warehouse cannot say -- the capability is optional and a
	// workbench without a tree still runs queries.
	Tree []Schema

	// TreeCut says the service cut the listing, which has to be drawn: a
	// browser silently showing half a warehouse would have somebody conclude
	// a table does not exist.
	TreeCut bool

	// Open is the one relation whose columns are drawn, as `schema.name`,
	// and empty when none is. ONE, because CHECKPOINT D made columns lazy:
	// a tree that drew every relation's would be the project-wide COLUMNS
	// query it refused, assembled one request at a time.
	Open string

	// Cols is what Open holds. Empty when the warehouse cannot say, which
	// is not an error -- Describer is optional.
	Cols []sqlserve.Column
}

// Schema is one group in the tree.
type Schema struct {
	Name   string
	Tables []string
}

// Warehouse is one connection, and one destination that reaches it.
type Warehouse struct {
	// Name is `scheme://connection`, which is what identifies a connection.
	Name string

	// Target is one whole destination on it, because that is what every
	// request carries.
	Target string
}

// Warehouses groups the catalog's destinations by the connection they live on.
//
// A TARGET IS NOT A CONNECTION, and the tree is about connections. The
// catalog lists `bigquery://acme/bronze/orders` and
// `bigquery://acme/silver/daily` as two destinations, and `serve` resolves
// BOTH to one connection: `sql/internal/serve/target.go` reads a target as
// `scheme://connection/schema/table` and opens on the scheme and the FIRST
// segment -- `/v1/query` never looks at the rest. A tree with one root per
// target would draw the same warehouse twice, with identical contents under
// each.
//
// THE SPLIT IS REPEATED HERE AND THAT IS A COST, stated rather than hidden.
// The engine cannot import that parser: it holds no warehouse code, and
// `engine-weight.sh` is what enforces it. What keeps the repetition honest is
// that this grouping is only a LABEL -- every request still carries a whole
// target, which `serve` parses itself -- so a grouping that got it wrong
// would draw a wrong heading and never wrong data.
func Warehouses(targets []string) []Warehouse {
	var out []Warehouse
	at := map[string]bool{}
	for _, t := range targets {
		scheme, rest, ok := strings.Cut(t, "://")
		if !ok {
			continue
		}
		name := scheme + "://" + rest
		if first, _, ok := strings.Cut(rest, "/"); ok {
			name = scheme + "://" + first
		}
		if at[name] {
			continue
		}
		at[name] = true
		out = append(out, Warehouse{Name: name, Target: t})
	}
	return out
}

// Branch is one connection's listing, which the page and the fragment
// endpoint both draw.
//
// ONE DEFINITION, TWO DOORS. The island asks for a connection's objects over
// `/api/sql/objects` and the first page view gets the same markup from the
// template; two pieces of markup for one tree is how the classes, the ARIA
// and the "this warehouse does not say" drift apart.
type Branch struct {
	Target string
	Tree   []Schema
	Cut    bool

	// Open is the relation whose columns are drawn on the server path, and
	// empty on the fragment -- the island opens its own.
	Open string
	Cols []sqlserve.Column
}

// Branch is what the active connection holds, for the page.
func (v SQLView) Branch() Branch {
	return Branch{Target: v.Target, Tree: v.Tree, Cut: v.TreeCut, Open: v.Open, Cols: v.Cols}
}

// Warehouses is the tree's roots.
func (v SQLView) Warehouses() []Warehouse { return Warehouses(v.Targets) }

// Active says this warehouse is the one whose listing is drawn.
func (v SQLView) Active(w Warehouse) bool {
	return v.Target != "" && (v.Target == w.Target || strings.HasPrefix(v.Target, w.Name+"/"))
}

// Running is the connection a query would run on, as the tree names it.
//
// THE CONNECTION AND NOT THE DESTINATION. `/v1/query` opens on the scheme
// and the first segment and never looks at the rest of a target, so
// `bigquery://acme` is the whole truth and `bigquery://acme/bronze/orders`
// would name a table the statement may not even mention. It is also the
// string the tree's root carries, so the label and the node agree -- which
// is what lets the island set one from the other.
func (v SQLView) Running() string {
	for _, w := range v.Warehouses() {
		if v.Active(w) {
			return w.Name
		}
	}
	return ""
}

// loaded is `data-loaded`, which the search box reads: a connection nobody
// opened holds names no filter can see, and a search that quietly skips one
// is how somebody concludes a table does not exist.
func loaded(v SQLView, w Warehouse) string {
	if v.Active(w) && len(v.Tree) > 0 {
		return "true"
	}
	return "false"
}

// expanded is a connection node's ARIA state.
func expanded(v SQLView, w Warehouse) string {
	if v.Active(w) {
		return "true"
	}
	return "false"
}

// BuildTree groups a listing by schema, in the order the service gave it.
//
// THE SERVICE ORDERS, NOT THIS. Both dialects sort in SQL, where the
// warehouse's own collation decides -- re-sorting here would be a second
// opinion about which of `Orders` and `orders` comes first.
func BuildTree(rels []sqlserve.Relation) []Schema {
	var out []Schema
	at := map[string]int{}
	for _, r := range rels {
		i, seen := at[r.Schema]
		if !seen {
			at[r.Schema] = len(out)
			out = append(out, Schema{Name: r.Schema})
			i = len(out) - 1
		}
		out[i].Tables = append(out[i].Tables, r.Name)
	}
	return out
}

// BuildSQL keeps only what can be queried.
func BuildSQL(entries []postgres.CatalogEntry) SQLView {
	v := SQLView{}
	seen := map[string]bool{}
	for _, e := range entries {
		if !catalog.IsRelation(e.Target) || seen[e.Target] {
			continue
		}
		seen[e.Target] = true
		v.Targets = append(v.Targets, e.Target)
	}
	return v
}

// Note is the line under the grid: what came back, what it cost, how long.
func (v SQLView) Note() string {
	if v.Result == nil {
		return ""
	}
	n := len(v.Result.Rows)
	rows := "rows"
	if n == 1 {
		rows = "row"
	}
	note := fmt.Sprintf("%d %s · %s scanned · %d ms", n, rows,
		bytesText(v.Result.Bytes), v.Result.Millis)
	if v.Result.Truncated {
		note += " — there are more"
	}
	return note
}

// Knows says the catalog lists this destination.
//
// ONE FUNCTION, TWO CALLERS, AND THAT IS THE POINT. `/sql`'s GET path had
// this loop written out and its POST path had nothing, so a destination the
// catalog did not list was refused in a link and accepted in a body. Two
// copies of a rule are two rules; the second one was missing for as long as
// the first one existed.
func (v SQLView) Knows(target string) bool {
	for _, t := range v.Targets {
		if t == target {
			return true
		}
	}
	return false
}

// Unknown is what the screen says when a body named something the catalog
// does not list.
//
// IT DOES NOT REPEAT WHAT WAS SENT. The string came from the request, and a
// page that echoes a request back is a page that has to be right about
// escaping forever. templ escapes it today; this does not need templ to be
// right.
const Unknown = "That is not a destination this console knows. " +
	"The catalog lists what Brevis has written, and nothing else can be queried here."

// Empty says there is nothing to query, which is a sentence and not a blank
// screen: a console with no relational destination has nothing to point this
// at, and the reason is the pipelines rather than the service.
func (v SQLView) Empty() bool { return len(v.Targets) == 0 }

// EditorAssets is the island's load order, exported so it can be pinned.
//
// THE ORDER IS NOT STYLE. The SQL mode calls `CodeMirror.defineMode` the
// instant it runs, and `CodeMirror` is what the first file defines; swapping
// the two leaves a plain textarea and a console error. The DAG island carries
// the same warning in prose -- "swapping two lines here leaves the screen
// blank" -- and nothing has ever checked it.
//
// No Go test can watch a browser evaluate these. What a test CAN do is refuse
// a reordering, which is the mistake the prose is about.
func EditorAssets() []string { return editorAssets.JS }

// openOf is the ARIA state of a relation's disclosure button, as a string
// because that is what the attribute takes.
//
// ON Branch AND NOT SQLView: the fragment endpoint draws the same tree with
// no page around it, and a helper that needed the whole view would have made
// one of the two callers build a view it does not have.
func openOf(b Branch, schema, table string) string {
	if b.Open == schema+"."+table {
		return "true"
	}
	return "false"
}
