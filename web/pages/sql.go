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
