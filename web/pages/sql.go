package pages

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
	"github.com/AreteAcademy/brevis/web/components"
	"github.com/AreteAcademy/brevis/web/i18n"
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
	// Preloaded says Statement is a table's first look from a link, not what
	// somebody ran: the island opens it in a tab of its own rather than over
	// the one somebody was writing in.
	Preloaded bool

	Result *sqlserve.Result
	// Refused is the service declining the statement, in its own words; the
	// page titles it (screen-sql T3). ErrKey is a failure the console words
	// itself, with ErrArg filling it; Err any other failure, as it came.
	Refused        string
	ErrKey, ErrArg string
	Err            string

	// Tree is what the chosen connection holds, grouped by schema. Empty
	// when the warehouse cannot say -- the capability is optional and a
	// workbench without a tree still runs queries.
	Tree []Schema

	// Writable says this connection accepts statements that CHANGE data,
	// as the service reported it. The console never decides this and never
	// reads the SQL to guess at it.
	Writable bool

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

// Warehouses is the picker's choices: one per connection.
func (v SQLView) Warehouses() []Warehouse { return Warehouses(v.Targets) }

// queryableSchemes are the warehouses `brevis-sql serve` has a dialect for
// (`sql/internal/dialect`). The catalog lists Redshift and MySQL tables too;
// the picker offers them disabled, with the reason, rather than a tree that
// answers nothing.
var queryableSchemes = map[string]bool{"bigquery": true, "postgres": true}

// Queryable says this console can read the destination.
func Queryable(target string) bool {
	scheme, _, ok := strings.Cut(target, "://")
	return ok && queryableSchemes[scheme]
}

// FirstQueryable is the destination the workbench opens on when it was given
// none: the first one it can read.
func (v SQLView) FirstQueryable() string {
	for _, t := range v.Targets {
		if Queryable(t) {
			return t
		}
	}
	return ""
}

// Unreadable is the kinds of warehouse in the catalog this console cannot
// read, as their makers write them, for the sentence under the picker.
func (v SQLView) Unreadable() []string {
	var out []string
	for _, w := range v.Warehouses() {
		scheme, _, _ := strings.Cut(w.Name, "://")
		if !queryableSchemes[scheme] && !slices.Contains(out, scheme) {
			out = append(out, scheme)
		}
	}
	return out
}

// SelectFor is a table's first look: every column, a hundred rows, the table
// named as a build names it -- `schema.table`, with no project or database,
// which is the connection's. "" for a destination that is not a table this
// console reads.
func SelectFor(target string) string {
	if !Queryable(target) {
		return ""
	}
	_, rest, _ := strings.Cut(target, "://")
	segs := strings.Split(rest, "/")
	if len(segs) != 3 || segs[1] == "" || segs[2] == "" {
		return ""
	}
	return selectOf(segs[1], segs[2])
}

func selectOf(schema, table string) string {
	return "SELECT *\nFROM " + schema + "." + table + "\nLIMIT 100;"
}

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

// Note is the line over the grid: what came back, what it read, how long --
// "2 rows · 91.9 MB read · 523 ms" -- in the reader's words and numbers.
func (v SQLView) Note(ctx context.Context) string {
	if v.Result == nil {
		return ""
	}
	if v.Result.Wrote {
		return v.Changed(ctx) + " · " + v.Cost(ctx)
	}
	note := i18n.N(ctx, "sql.result.rows", len(v.Result.Rows)) + " · " + v.Cost(ctx)
	if v.Result.Truncated {
		note += " " + i18n.T(ctx, "sql.result.more")
	}
	return note
}

// Changed is what a statement that wrote did, in one sentence.
//
// THREE ANSWERS AND NOT TWO. "12 rows changed" and "0 rows changed" are the
// same sentence with a different number, and both are a COUNT. "no rows to
// count" is not a count at all -- a CREATE TABLE changed nothing and did not
// fail, and printing zero for it reads as a failure to whoever ran it.
func (v SQLView) Changed(ctx context.Context) string {
	if v.Result == nil || !v.Result.Wrote {
		return ""
	}
	if v.Result.Changed == nil {
		return i18n.T(ctx, "sql.result.uncounted")
	}
	return i18n.N(ctx, "sql.result.changed", int(*v.Result.Changed))
}

// Cost is what the statement spent: "91.9 MB read · 523 ms".
//
// ONE FORMATTER, TWO READERS. History lists the same figures, and it is
// built in the browser -- so the alternative was a second `bytesText` in
// JavaScript. Two rounders disagree eventually.
func (v SQLView) Cost(ctx context.Context) string {
	if v.Result == nil {
		return ""
	}
	return i18n.T(ctx, "sql.result.read", "bytes", bytesSaid(ctx, v.Result.Bytes)) + " · " +
		i18n.Number(ctx, v.Result.Millis) + " ms"
}

// Ran says something happened, which is the difference between a screen
// waiting for a first query and one showing the second.
func (v SQLView) Ran() bool { return v.Result != nil || v.failed() }

// failed says the statement came back with no answer, for whatever reason.
func (v SQLView) failed() bool { return v.Err != "" || v.Refused != "" || v.ErrKey != "" }

// Outcome is what the Recent rail records.
//
// A REFUSAL IS AN OUTCOME. A rail that only listed what succeeded would be a
// rail somebody reads to conclude a query was never run -- and the refused
// ones are the ones worth finding again.
func (v SQLView) Outcome() string {
	if v.Result != nil {
		return "ok"
	}
	if v.failed() {
		return "failed"
	}
	return ""
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

// ConnectionOf is the connection a destination lives on, as `serve` reads
// it: the first segment after the scheme -- what a missing `brevis.yaml`
// entry is named by.
func ConnectionOf(target string) string {
	_, rest, ok := strings.Cut(target, "://")
	if !ok {
		return target
	}
	first, _, _ := strings.Cut(rest, "/")
	return first
}

// JSON is the result as the service sent it, for the tab of that name.
//
// THE ROWS AND THE COLUMNS, AND NOT THE STATEMENT. A JSON view is for
// pasting into something else; the query that produced it is the one thing
// on this screen that must not travel, and a reader who wants it has it in
// the box above.
func (v SQLView) JSON() string {
	if v.Result == nil {
		return ""
	}
	out, err := json.MarshalIndent(struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{v.Result.Columns, v.Result.Rows}, "", "  ")
	if err != nil {
		// Unreachable with these types, and an error here is not worth a
		// screen: the tab beside this one holds the same answer.
		return ""
	}
	return string(out)
}

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

// warehouseLabel is a connection as the picker says it: the warehouse by its
// maker's name, then the connection -- "Google BigQuery · acme".
func warehouseLabel(ctx context.Context, w Warehouse) string {
	scheme, rest, _ := strings.Cut(w.Name, "://")
	return components.LogoTitle(ctx, kindLogo(scheme)) + " · " + rest
}

// unreadableSaid is the kinds the console cannot read, by their makers'
// names: "Amazon Redshift, MySQL".
func unreadableSaid(ctx context.Context, kinds []string) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = components.LogoTitle(ctx, kindLogo(k))
	}
	return strings.Join(names, ", ")
}

// bytesSaid is a byte count as the reader writes numbers: "273.9 MB" in
// English, "273,9 MB" in Portuguese. The units are the same everywhere.
func bytesSaid(ctx context.Context, n int64) string {
	for _, u := range []struct {
		size int64
		unit string
	}{{1 << 40, "TB"}, {1 << 30, "GB"}, {1 << 20, "MB"}, {1 << 10, "kB"}} {
		if n >= u.size {
			return i18n.Decimal(ctx, float64(n)/float64(u.size), 1) + " " + u.unit
		}
	}
	return i18n.Number(ctx, n) + " B"
}
