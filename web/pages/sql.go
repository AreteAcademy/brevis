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

// Empty says there is nothing to query, which is a sentence and not a blank
// screen: a console with no relational destination has nothing to point this
// at, and the reason is the pipelines rather than the service.
func (v SQLView) Empty() bool { return len(v.Targets) == 0 }

// Short is a destination without its scheme, for a picker that has to fit.
func Short(target string) string {
	_, rest, ok := strings.Cut(target, "://")
	if !ok {
		return target
	}
	return rest
}

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

// open is the ARIA state of a relation's disclosure button, as a string
// because that is what the attribute takes.
func open(v SQLView, schema, table string) string {
	if v.Open == schema+"."+table {
		return "true"
	}
	return "false"
}
